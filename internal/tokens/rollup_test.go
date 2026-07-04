package tokens

import (
	"testing"
	"time"
)

func cellByKey(cells []DayPhaseCost) map[string]DayPhaseCost {
	m := make(map[string]DayPhaseCost, len(cells))
	for _, c := range cells {
		m[c.Date+"/"+c.Phase] = c
	}
	return m
}

// TestRollupBucketsByDayAndPhase is the core rollup contract: calls logged across
// several days and buckets sum into one cell per (date, phase), and a line whose
// date falls outside [from, to] is excluded entirely.
func TestRollupBucketsByDayAndPhase(t *testing.T) {
	dir := t.TempDir()
	day := func(d int) func() time.Time {
		return fixedClock(time.Date(2026, 7, d, 9, 0, 0, 0, time.UTC))
	}

	s := New(dir).WithClock(day(1))
	s.SetTicket("COD-1")
	s.Append("build", Record{Input: 100, Output: 50, CostUSD: ptr(0.10)})
	s.Append("build", Record{Input: 200, Output: 60, CostUSD: ptr(0.20)})
	s.Append("verify", Record{Input: 40, Output: 20, CostUSD: ptr(0.05)})

	s = New(dir).WithClock(day(2))
	s.SetTicket("COD-2")
	s.Append("build", Record{Input: 10, Output: 5, CostUSD: ptr(0.01)})

	// A call outside the window must not appear.
	s = New(dir).WithClock(day(9))
	s.SetTicket("COD-9")
	s.Append("build", Record{Input: 999, Output: 999, CostUSD: ptr(9.99)})

	cells := New(dir).Rollup("2026-07-01", "2026-07-03")
	if len(cells) != 3 {
		t.Fatalf("Rollup returned %d cells, want 3 (build+verify on day 1, build on day 2): %+v", len(cells), cells)
	}
	by := cellByKey(cells)

	d1build := by["2026-07-01/build"]
	if d1build.Tokens != 410 || !approx(d1build.Cost, 0.30) || !d1build.Metered {
		t.Errorf("day-1 build = %+v, want tokens 410 cost 0.30 metered", d1build)
	}
	if d1verify := by["2026-07-01/verify"]; d1verify.Tokens != 60 || !approx(d1verify.Cost, 0.05) {
		t.Errorf("day-1 verify = %+v, want tokens 60 cost 0.05", d1verify)
	}
	if d2build := by["2026-07-02/build"]; d2build.Tokens != 15 || !approx(d2build.Cost, 0.01) {
		t.Errorf("day-2 build = %+v, want tokens 15 cost 0.01", d2build)
	}
	if _, ok := by["2026-07-09/build"]; ok {
		t.Error("day-9 build leaked into a window that ends 2026-07-03")
	}
}

// TestRollupMeteredLowerBound confirms a cell mixing a metered and an unmetered
// call (a subscription phase that logs tokens but no dollars) marks itself
// unmetered, so the summed cost reads as a lower bound.
func TestRollupMeteredLowerBound(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-3")
	s.Append("build", Record{Input: 100, Output: 50, CostUSD: ptr(0.10)})
	s.Append("build", Record{Input: 100, Output: 50}) // no per-call cost

	cells := New(dir).Rollup("2026-07-01", "2026-07-01")
	if len(cells) != 1 {
		t.Fatalf("Rollup returned %d cells, want 1", len(cells))
	}
	if c := cells[0]; c.Metered || c.Tokens != 300 || !approx(c.Cost, 0.10) {
		t.Errorf("cell = %+v, want tokens 300 cost 0.10 unmetered", c)
	}
}

// TestRollupEmpty covers a runs root with no logs in the window: nil, no error.
func TestRollupEmpty(t *testing.T) {
	if cells := New(t.TempDir()).Rollup("2026-07-01", "2026-07-31"); cells != nil {
		t.Errorf("Rollup over empty root = %+v, want nil", cells)
	}
}

// TestRollupDetailKeepsProviderAndModel confirms the detail rollup splits cells by
// (date, provider, model, phase), resolving the provider inline when recorded and
// falling back to model derivation for lines that predate the inline field.
func TestRollupDetailKeepsProviderAndModel(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-1")
	s.Append("build", Record{Input: 100, Output: 50, CostUSD: ptr(0.10), Model: "claude-opus-4-8"})
	s.Append("build", Record{Input: 40, Output: 20, CostUSD: ptr(0.05), Model: "gpt-5.4"})
	s.Append("verify", Record{Input: 200, Output: 100, CostUSD: ptr(0.20), Provider: "kimi", Model: "turbo"})

	cells := New(dir).RollupDetail("2026-07-01", "2026-07-01")
	if len(cells) != 3 {
		t.Fatalf("RollupDetail = %d cells, want 3 (one per provider/model/phase): %+v", len(cells), cells)
	}
	by := map[string]DetailCost{}
	for _, c := range cells {
		by[c.Provider+"/"+c.Model] = c
	}
	if c := by["claude/claude-opus-4-8"]; c.Phase != "build" || c.Tokens != 150 || !approx(c.Cost, 0.10) {
		t.Errorf("opus cell = %+v, want build tokens 150 cost 0.10 provider claude", c)
	}
	if c := by["codex/gpt-5.4"]; c.Tokens != 60 || !approx(c.Cost, 0.05) {
		t.Errorf("gpt cell = %+v, want tokens 60 cost 0.05 provider codex (derived)", c)
	}
	if c := by["kimi/turbo"]; c.Tokens != 300 {
		t.Errorf("kimi cell = %+v, want the inline provider to win over the unmappable model", c)
	}
}

func TestProviderForModel(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":    "claude",
		"claude-sonnet-5":    "claude",
		"claude-haiku-4-5":   "claude",
		"claude-fable-5":     "claude",
		"gpt-5.4":            "codex",
		"gpt-5.4-mini":       "codex",
		"kimi-k2":            "kimi",
		"moonshot-v1":        "kimi",
		"":                   "",
		"some-unknown-model": "",
	}
	for model, want := range cases {
		if got := ProviderForModel(model); got != want {
			t.Errorf("ProviderForModel(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestAnomaliesRoundTrip confirms the reader returns the same trips Flag wrote to
// anomalies.jsonl, and nil for a ticket that never tripped a threshold.
func TestAnomaliesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-88")
	s.Append("cleanup", Record{Output: 115_000, Turns: 8, CostUSD: ptr(6.24)})
	s.Flag("COD-88")

	got := New(dir).Anomalies("COD-88")
	if len(got) != 1 || got[0].Phase != "cleanup" || got[0].Cost != 6.24 || len(got[0].Reasons) != 2 {
		t.Errorf("Anomalies = %+v, want the single cleanup trip with two reasons", got)
	}
	if quiet := New(dir).Anomalies("COD-404"); quiet != nil {
		t.Errorf("Anomalies for an unflagged ticket = %+v, want nil", quiet)
	}
}
