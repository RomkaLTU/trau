package state

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// --- Idx / Terminal -------------------------------------------------------

func TestIdxRanking(t *testing.T) {
	tests := []struct {
		phase string
		want  int
	}{
		{Building, 1}, {Built, 2}, {HandedOff, 3}, {Verified, 4},
		{PROpen, 5}, {Merged, 6}, {Quarantined, 9},
		{"", 0}, {"bogus", 0},
	}
	for _, tc := range tests {
		if got := Idx(tc.phase); got != tc.want {
			t.Errorf("Idx(%q) = %d, want %d", tc.phase, got, tc.want)
		}
	}
	// The ordered in-flight phases must rank strictly increasing.
	order := []string{Building, Built, HandedOff, Verified, PROpen, Merged}
	for i := 1; i < len(order); i++ {
		if Idx(order[i]) <= Idx(order[i-1]) {
			t.Errorf("phase order broken: Idx(%s) !> Idx(%s)", order[i], order[i-1])
		}
	}
}

func TestTerminal(t *testing.T) {
	for _, p := range []string{Merged, Quarantined} {
		if !Terminal(p) {
			t.Errorf("Terminal(%q) = false, want true", p)
		}
	}
	for _, p := range []string{Building, Built, HandedOff, Verified, PROpen, "", "bogus"} {
		if Terminal(p) {
			t.Errorf("Terminal(%q) = true, want false", p)
		}
	}
}

// --- Get / Set ------------------------------------------------------------

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir()).WithClock(fixedClock(time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)))
}

func TestSetGetRoundTrip(t *testing.T) {
	s := newStore(t)
	if err := s.Set("COD-1", "PHASE", Built); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("COD-1", "PHASE"); got != Built {
		t.Errorf("Get PHASE = %q, want %q", got, Built)
	}
	if got := s.Get("COD-1", "MISSING"); got != "" {
		t.Errorf("missing key = %q, want empty", got)
	}
	if got := s.Get("COD-404", "PHASE"); got != "" {
		t.Errorf("missing ticket = %q, want empty", got)
	}
}

func TestSetLastWriteWinsAndPreservesOthers(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PHASE", Building)
	_ = s.Set("COD-1", "BRANCH", "feature/COD-1-x")
	_ = s.Set("COD-1", "PHASE", Verified) // overwrite

	if got := s.Get("COD-1", "PHASE"); got != Verified {
		t.Errorf("PHASE = %q, want %q (last write wins)", got, Verified)
	}
	if got := s.Get("COD-1", "BRANCH"); got != "feature/COD-1-x" {
		t.Errorf("BRANCH = %q, want it preserved across the PHASE overwrite", got)
	}
	// Exactly one PHASE= line should remain in the file.
	data, _ := os.ReadFile(filepath.Join(s.Root(), "COD-1", "state"))
	if n := strings.Count(string(data), "PHASE="); n != 1 {
		t.Errorf("found %d PHASE= lines, want exactly 1", n)
	}
}

func TestSetValueWithEquals(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PR_URL", "https://x/pr?a=b&c=d")
	if got := s.Get("COD-1", "PR_URL"); got != "https://x/pr?a=b&c=d" {
		t.Errorf("value with '=' = %q, want it intact", got)
	}
}

func TestSetRefreshesUpdated(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PHASE", Building)
	if got := s.Get("COD-1", "UPDATED"); got != "2026-06-24 12:00:00" {
		t.Errorf("UPDATED = %q, want the clock-stamped value", got)
	}
}

func TestRemoveStateIdempotent(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PHASE", Built)
	if err := s.RemoveState("COD-1"); err != nil {
		t.Fatalf("RemoveState: %v", err)
	}
	if got := s.Get("COD-1", "PHASE"); got != "" {
		t.Errorf("state survived removal: %q", got)
	}
	// Removing again is not an error.
	if err := s.RemoveState("COD-1"); err != nil {
		t.Errorf("second RemoveState should be a no-op, got %v", err)
	}
}

// --- ResumeTarget ---------------------------------------------------------

func TestResumeTargetOrdersByNumberNotLexicographically(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-10", "PHASE", Building) // in-flight, num 10
	_ = s.Set("COD-2", "PHASE", Verified)  // in-flight, num 2
	_ = s.Set("COD-9", "PHASE", Merged)    // terminal — skipped

	id, phase := s.ResumeTarget()
	if id != "COD-2" || phase != Verified {
		t.Errorf("ResumeTarget = (%q,%q), want (COD-2, verified) — lowest NUMBER, not lexicographic", id, phase)
	}
}

func TestResumeTargetSkipsTerminalAndUnknown(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PHASE", Merged)
	_ = s.Set("COD-2", "PHASE", Quarantined)
	_ = s.Set("COD-3", "PHASE", "garbage") // rank 0 — skipped

	if id, phase := s.ResumeTarget(); id != "" || phase != "" {
		t.Errorf("ResumeTarget = (%q,%q), want empty when nothing is in-flight", id, phase)
	}
}

func TestTicketsExcludesUnstated(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PHASE", Built)
	_ = s.Set("COD-2", "PHASE", Built)
	got := s.Tickets()
	if len(got) != 2 {
		t.Fatalf("Tickets() = %v, want 2 entries", got)
	}
}

func TestTicketNum(t *testing.T) {
	tests := []struct {
		id  string
		num int
		ok  bool
	}{
		{"COD-123", 123, true},
		{"COD-2", 2, true},
		{"TMS-42-extra-9", 9, true}, // last numeric run wins
		{"nonum", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		n, ok := ticketNum(tc.id)
		if n != tc.num || ok != tc.ok {
			t.Errorf("ticketNum(%q) = (%d,%v), want (%d,%v)", tc.id, n, ok, tc.num, tc.ok)
		}
	}
}

// --- Status (golden) ------------------------------------------------------

func TestStatusGolden(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-10", "PHASE", Building)
	_ = s.Set("COD-2", "PHASE", Verified)
	_ = s.Set("COD-2", "PR_URL", "https://example/pr/1")

	totals := map[string]struct {
		tok     int
		cost    float64
		metered bool
	}{
		"COD-10": {50, 0, true},
		"COD-2":  {1200, 0.12, true},
	}
	total := func(id string) (int, float64, bool) {
		v := totals[id]
		return v.tok, v.cost, v.metered
	}

	var buf bytes.Buffer
	s.Status(&buf, total)

	want, err := os.ReadFile(filepath.Join("testdata", "status_rows.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Errorf("Status output mismatch.\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}

func TestStatusEmptyGolden(t *testing.T) {
	s := newStore(t)
	var buf bytes.Buffer
	s.Status(&buf, func(string) (int, float64, bool) { return 0, 0, true })

	raw, err := os.ReadFile(filepath.Join("testdata", "status_empty.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(string(raw), "{{ROOT}}", s.Root())
	if buf.String() != want {
		t.Errorf("empty Status mismatch.\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}
}
