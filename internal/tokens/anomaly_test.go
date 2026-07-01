package tokens

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// TestFlagTripsHotPhases is the core case: a ticket whose cleanup blew past the
// cost/output rails and whose commit blew past the turn rail both trip, each
// carrying only the reasons it actually cleared, while a modest build phase does
// not — and the trips land in anomalies.jsonl.
func TestFlagTripsHotPhases(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-88")
	s.Append("build", Record{Output: 5_000, Turns: 6, CostUSD: ptr(0.80)})     // under every rail
	s.Append("cleanup", Record{Output: 115_000, Turns: 8, CostUSD: ptr(6.24)}) // output + cost
	s.Append("commit", Record{Output: 4_000, Turns: 58, CostUSD: ptr(0.50)})   // turns only

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

// TestFlagQuietTicket confirms a ticket whose every phase stays under the rails
// produces no anomalies and no file.
func TestFlagQuietTicket(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-91")
	s.Append("build", Record{Output: 8_000, Turns: 12, CostUSD: ptr(1.50)})
	s.Append("commit", Record{Output: 3_000, Turns: 9, CostUSD: ptr(0.40)})

	if got := s.Flag("COD-91"); got != nil {
		t.Errorf("Flag = %+v, want nil (all phases under rails)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "COD-91", "anomalies.jsonl")); !os.IsNotExist(err) {
		t.Errorf("anomalies.jsonl should not exist (err=%v)", err)
	}
}

// TestFlagMissingTokensFile covers the no-tokens.jsonl path (nothing recorded):
// Flag returns nil without error and writes no file.
func TestFlagMissingTokensFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))

	if got := s.Flag("COD-92"); got != nil {
		t.Errorf("Flag = %+v, want nil (no tokens.jsonl)", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "COD-92", "anomalies.jsonl")); !os.IsNotExist(err) {
		t.Errorf("anomalies.jsonl should not exist (err=%v)", err)
	}
}
