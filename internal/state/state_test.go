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

// --- Reconcilable / StaleCheckpoint --------------------------------------

func TestReconcilable(t *testing.T) {
	// In-flight (rank 1–5) and quarantined (9) are candidates; merged (6) and
	// unknown/empty (0) are not.
	for _, p := range []string{Building, Built, HandedOff, Verified, PROpen, Quarantined} {
		if !Reconcilable(p) {
			t.Errorf("Reconcilable(%q) = false, want true", p)
		}
	}
	for _, p := range []string{Merged, "", "bogus"} {
		if Reconcilable(p) {
			t.Errorf("Reconcilable(%q) = true, want false", p)
		}
	}
}

func TestStaleCheckpoint(t *testing.T) {
	tests := []struct {
		name        string
		phase       string
		trackerDone bool
		want        bool
	}{
		{"quarantined-but-done is stale", Quarantined, true, true},
		{"in-flight-but-done is stale", Built, true, true},
		{"in-flight pr_open but done is stale", PROpen, true, true},
		{"still-open quarantined is left intact", Quarantined, false, false},
		{"still-open in-flight is left intact", Built, false, false},
		{"merged is never stale even if done", Merged, true, false},
		{"unknown phase is never stale", "", true, false},
	}
	for _, tc := range tests {
		if got := StaleCheckpoint(tc.phase, tc.trackerDone); got != tc.want {
			t.Errorf("%s: StaleCheckpoint(%q, %v) = %v, want %v", tc.name, tc.phase, tc.trackerDone, got, tc.want)
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

func TestResumeTargetFuncSkipsOutOfScope(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-100", "PHASE", Building) // lower number, but NOT in the epic
	_ = s.Set("COD-200", "PHASE", Building) // the epic's child

	// The incident: a stale checkpoint for an unrelated ticket (COD-100) sits in the
	// same runs/ dir. Unfiltered, it wins (lowest number) and hijacks the run.
	if id, _ := s.ResumeTargetFunc(nil); id != "COD-100" {
		t.Fatalf("ResumeTargetFunc(nil) = %q, want COD-100 (unfiltered picks lowest)", id)
	}

	// Scoped to the epic's children, the unrelated checkpoint is skipped and the
	// child resumes instead.
	keep := func(id string) bool { return id == "COD-200" }
	if id, phase := s.ResumeTargetFunc(keep); id != "COD-200" || phase != Building {
		t.Errorf("ResumeTargetFunc(keep COD-200) = (%q,%q), want (COD-200, building)", id, phase)
	}

	// A filter that excludes everything proceeds to Pick (empty resume target).
	none := func(string) bool { return false }
	if id, phase := s.ResumeTargetFunc(none); id != "" || phase != "" {
		t.Errorf("ResumeTargetFunc(none) = (%q,%q), want empty", id, phase)
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
	_ = s.Set("COD-2", "ANOMALIES", "2")

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

// TestStatusPlanningBucket checks a non-ticket bucket (planning) with spend is
// rendered as its own row and folded into the grand total, while a bucket with no
// spend is omitted entirely.
func TestStatusPlanningBucket(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PHASE", Verified)

	total := func(id string) (int, float64, bool) {
		switch id {
		case "COD-1":
			return 1000, 0.10, true
		case "_plans":
			return 500, 0.05, true
		}
		return 0, 0, true
	}

	var buf bytes.Buffer
	s.Status(&buf, total, Bucket{ID: "_plans", Label: "planning"}, Bucket{ID: "_empty", Label: "empty"})
	out := buf.String()

	if !strings.Contains(out, "planning") {
		t.Errorf("planning row missing:\n%s", out)
	}
	if strings.Contains(out, "empty") {
		t.Errorf("a zero-spend bucket must not render:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL") || !strings.Contains(out, "1500") {
		t.Errorf("grand total should fold in the planning bucket (1500):\n%s", out)
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

func TestUnsetRemovesKeyAndPreservesOthers(t *testing.T) {
	s := newStore(t)
	_ = s.Set("COD-1", "PHASE", Merged)
	_ = s.Set("COD-1", "FAILURE_REASON", "unexpected error during CI/merge: boom")

	if err := s.Unset("COD-1", "FAILURE_REASON"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("COD-1", "FAILURE_REASON"); got != "" {
		t.Errorf("FAILURE_REASON = %q after Unset, want empty", got)
	}
	if got := s.Get("COD-1", "PHASE"); got != Merged {
		t.Errorf("PHASE = %q, want it preserved across the Unset", got)
	}
	data, _ := os.ReadFile(filepath.Join(s.Root(), "COD-1", "state"))
	if strings.Contains(string(data), "FAILURE_REASON") {
		t.Errorf("state file still mentions FAILURE_REASON:\n%s", data)
	}
	if !strings.Contains(string(data), "UPDATED=") {
		t.Errorf("state file lost its UPDATED stamp:\n%s", data)
	}
}

func TestUnsetAbsentKeyOrFileIsNoOp(t *testing.T) {
	s := newStore(t)
	if err := s.Unset("COD-404", "FAILURE_REASON"); err != nil {
		t.Fatalf("Unset on a missing file = %v, want nil", err)
	}
	_ = s.Set("COD-1", "PHASE", Built)
	before, _ := os.ReadFile(filepath.Join(s.Root(), "COD-1", "state"))
	if err := s.Unset("COD-1", "FAILURE_REASON"); err != nil {
		t.Fatalf("Unset on an absent key = %v, want nil", err)
	}
	after, _ := os.ReadFile(filepath.Join(s.Root(), "COD-1", "state"))
	if string(before) != string(after) {
		t.Errorf("Unset of an absent key rewrote the file:\nbefore: %s\nafter: %s", before, after)
	}
}
