package tokens

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeVerdict(t *testing.T, root, id string, fits bool) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"fits_one_window":%v,"reason":"x","suggested_slices":[]}`, fits)
	if err := os.WriteFile(filepath.Join(dir, "sizejudge.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func anomalyLineCount(t *testing.T, root, id string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, id, "anomalies.jsonl"))
	if err != nil {
		return 0
	}
	var n int
	for _, ln := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(ln)) > 0 {
			n++
		}
	}
	return n
}

// TestFlagTripsHotPhasesOnOneWindowTicket is the core case: a one-window ticket
// whose cleanup blew past the cost/output rails and whose commit blew past the turn
// rail both trip, each carrying only the reasons it actually cleared, while a
// modest build phase does not — and the trips land in anomalies.jsonl.
func TestFlagTripsHotPhasesOnOneWindowTicket(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-88")
	s.Append("build", Record{Output: 5_000, Turns: 6, CostUSD: ptr(0.80)})     // under every rail
	s.Append("cleanup", Record{Output: 115_000, Turns: 8, CostUSD: ptr(6.24)}) // output + cost
	s.Append("commit", Record{Output: 4_000, Turns: 58, CostUSD: ptr(0.50)})   // turns only
	writeVerdict(t, dir, "COD-88", true)

	got := s.Flag("COD-88")
	if len(got) != 2 {
		t.Fatalf("Flag returned %d anomalies, want 2 (cleanup, commit): %+v", len(got), got)
	}
	if got[0].Phase != "cleanup" {
		t.Errorf("anomaly[0].Phase = %q, want cleanup", got[0].Phase)
	}
	joined0 := strings.Join(got[0].Reasons, "; ")
	if !strings.Contains(joined0, "cost") || !strings.Contains(joined0, "output") {
		t.Errorf("cleanup reasons = %v, want cost + output", got[0].Reasons)
	}
	if strings.Contains(joined0, "turns") {
		t.Errorf("cleanup reasons = %v, should not include turns (8 is under)", got[0].Reasons)
	}
	if got[1].Phase != "commit" {
		t.Errorf("anomaly[1].Phase = %q, want commit", got[1].Phase)
	}
	if joined1 := strings.Join(got[1].Reasons, "; "); !strings.Contains(joined1, "turns") || strings.Contains(joined1, "output") {
		t.Errorf("commit reasons = %v, want turns only", got[1].Reasons)
	}
	if n := anomalyLineCount(t, dir, "COD-88"); n != 2 {
		t.Errorf("anomalies.jsonl has %d lines, want 2", n)
	}
}

// TestFlagIgnoresLargeAndUnverdictedTickets covers the false-positive guard: the
// same hot spend must NOT trip when the size judge called the ticket not-one-window
// (a legitimately large ticket) or when no durable verdict exists (size judge off /
// pre-durable run) — and neither path writes anomalies.jsonl.
func TestFlagIgnoresLargeAndUnverdictedTickets(t *testing.T) {
	cases := []struct {
		name    string
		verdict string // "large" | "missing"
	}{
		{name: "large ticket does not trip", verdict: "large"},
		{name: "missing verdict does not trip", verdict: "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
			s.SetTicket("COD-90")
			s.Append("cleanup", Record{Output: 115_000, Turns: 58, CostUSD: ptr(6.24)})
			if tc.verdict == "large" {
				writeVerdict(t, dir, "COD-90", false)
			}

			if got := s.Flag("COD-90"); got != nil {
				t.Errorf("Flag = %+v, want nil (%s)", got, tc.name)
			}
			if _, err := os.Stat(filepath.Join(dir, "COD-90", "anomalies.jsonl")); !os.IsNotExist(err) {
				t.Errorf("anomalies.jsonl should not exist (err=%v)", err)
			}
		})
	}
}

// TestFlagQuietOneWindowTicket confirms a one-window ticket whose every phase stays
// under the rails produces no anomalies and no file.
func TestFlagQuietOneWindowTicket(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-91")
	s.Append("build", Record{Output: 8_000, Turns: 12, CostUSD: ptr(1.50)})
	s.Append("commit", Record{Output: 3_000, Turns: 9, CostUSD: ptr(0.40)})
	writeVerdict(t, dir, "COD-91", true)

	if got := s.Flag("COD-91"); got != nil {
		t.Errorf("Flag = %+v, want nil (all phases under rails)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "COD-91", "anomalies.jsonl")); !os.IsNotExist(err) {
		t.Errorf("anomalies.jsonl should not exist (err=%v)", err)
	}
}
