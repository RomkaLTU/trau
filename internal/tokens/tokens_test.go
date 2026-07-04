package tokens

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ptr(v float64) *float64 { return &v }

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// --- Append + Total -------------------------------------------------------

func TestAppendTotalFormulaExcludesReasoning(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-1")

	// total = input + output + cache_read + cache_creation (reasoning is NOT summed).
	s.Append("build", Record{Input: 100, Output: 50, CacheRead: 10, CacheCreation: 5, Reasoning: 9999, CostUSD: ptr(0.10)})

	tok, cost, metered := s.Total("COD-1")
	if tok != 165 {
		t.Errorf("tokens = %d, want 165 (reasoning excluded)", tok)
	}
	if cost != 0.10 {
		t.Errorf("cost = %v, want 0.10", cost)
	}
	if !metered {
		t.Error("metered should be true when every line carried a cost")
	}
}

func TestAppendDropsZeroTotal(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-1")

	// Only reasoning set → total 0 → dropped (no file, nothing logged).
	s.Append("build", Record{Reasoning: 500})
	if _, err := os.Stat(filepath.Join(dir, "COD-1", "tokens.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("zero-total call should not create a ledger file (err=%v)", err)
	}
	tok, _, _ := s.Total("COD-1")
	if tok != 0 {
		t.Errorf("tokens = %d, want 0", tok)
	}
}

func TestTotalMeteredLowerBound(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-1")

	s.Append("build", Record{Input: 100, CostUSD: ptr(0.20)})
	s.Append("verify", Record{Input: 100}) // no cost → subscription call

	tok, cost, metered := s.Total("COD-1")
	if tok != 200 {
		t.Errorf("tokens = %d, want 200", tok)
	}
	if cost != 0.20 {
		t.Errorf("cost = %v, want 0.20 (lower bound)", cost)
	}
	if metered {
		t.Error("metered should be FALSE when any line lacked a per-call cost")
	}
}

func TestTotalRoundsToCents(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)))
	s.SetTicket("COD-1")
	s.Append("a", Record{Input: 1, CostUSD: ptr(0.014)})
	s.Append("b", Record{Input: 1, CostUSD: ptr(0.014)})

	_, cost, _ := s.Total("COD-1")
	if cost != 0.03 {
		t.Errorf("cost = %v, want 0.03 (0.028 rounded once to cents)", cost)
	}
}

func TestTotalMissingFileIsZero(t *testing.T) {
	s := New(t.TempDir())
	tok, cost, metered := s.Total("nope")
	if tok != 0 || cost != 0 || !metered {
		t.Errorf("missing ledger = (%d,%v,%v), want (0,0,true)", tok, cost, metered)
	}
}

func TestTotalSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "COD-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"phase":"build","total":100,"cost_usd":0.1}
this is not json

{"phase":"verify","total":50,"cost_usd":0.05}
{broken
`
	if err := os.WriteFile(filepath.Join(dir, "COD-1", "tokens.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	tok, cost, _ := s.Total("COD-1")
	if tok != 150 {
		t.Errorf("tokens = %d, want 150 (malformed lines skipped)", tok)
	}
	if cost != 0.15 {
		t.Errorf("cost = %v, want 0.15", cost)
	}
}

func TestAppendBucketRouting(t *testing.T) {
	dir := t.TempDir()
	s := New(dir).WithClock(fixedClock(time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)))

	// No ticket set → falls back to the _loop bucket.
	s.Append("pick", Record{Input: 10, CostUSD: ptr(0.01)})
	if _, err := os.Stat(filepath.Join(dir, "_loop", "tokens.jsonl")); err != nil {
		t.Fatalf("loop-bucket ledger missing: %v", err)
	}

	s.SetTicket("COD-7")
	s.Append("build", Record{Input: 10, CostUSD: ptr(0.01)})
	if _, err := os.Stat(filepath.Join(dir, "COD-7", "tokens.jsonl")); err != nil {
		t.Fatalf("ticket-bucket ledger missing: %v", err)
	}

	// Clearing the ticket routes back to _loop.
	s.SetTicket("")
	s.Append("pick", Record{Input: 10, CostUSD: ptr(0.01)})
	if tok, _, _ := s.Total("_loop"); tok != 20 {
		t.Errorf("_loop tokens = %d, want 20", tok)
	}
}

// TestPlanBucketRouting checks planning calls land in the dedicated planning
// bucket: SetTicket(PlanBucket) points appends at runs/_plans/tokens.jsonl, its
// spend sums under Total(PlanBucket), and it counts toward the day window like any
// other bucket — the ledger side of the planning-parity requirement.
func TestPlanBucketRouting(t *testing.T) {
	dir := t.TempDir()
	clk := time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)
	s := New(dir).WithClock(func() time.Time { return clk })

	s.SetTicket(PlanBucket)
	s.Append("plan", Record{Input: 200, Output: 100, CostUSD: ptr(0.30)})
	s.Append("plan", Record{Input: 100, CostUSD: ptr(0.10)})

	if _, err := os.Stat(filepath.Join(dir, PlanBucket, "tokens.jsonl")); err != nil {
		t.Fatalf("planning-bucket ledger missing: %v", err)
	}
	tok, cost, metered := s.Total(PlanBucket)
	if tok != 400 {
		t.Errorf("planning tokens = %d, want 400", tok)
	}
	if cost != 0.40 {
		t.Errorf("planning cost = %v, want 0.40", cost)
	}
	if !metered {
		t.Error("metered should be true (every planning line carried a cost)")
	}

	dt, dc, _ := s.DayTotal("2026-06-24")
	if dt != 400 || dc != 0.40 {
		t.Errorf("day total = (%d, %v), want (400, 0.4) — planning counts toward the day", dt, dc)
	}
}

// --- DayTotal -------------------------------------------------------------

func TestDayTotalFiltersByDateAcrossBuckets(t *testing.T) {
	dir := t.TempDir()
	clk := time.Date(2026, 6, 24, 8, 0, 0, 0, time.UTC)
	s := New(dir).WithClock(func() time.Time { return clk })

	s.SetTicket("COD-1")
	s.Append("build", Record{Input: 100, CostUSD: ptr(0.10)}) // 06-24

	s.SetTicket("") // loop bucket, same day — still counts toward the day window
	s.Append("pick", Record{Input: 50, CostUSD: ptr(0.05)})

	clk = time.Date(2026, 6, 23, 8, 0, 0, 0, time.UTC)
	s.SetTicket("COD-1")
	s.Append("verify", Record{Input: 999, CostUSD: ptr(9.99)}) // 06-23, excluded

	tok, cost, metered := s.DayTotal("2026-06-24")
	if tok != 150 {
		t.Errorf("day tokens = %d, want 150 (only 06-24 lines, across buckets)", tok)
	}
	if cost != 0.15 {
		t.Errorf("day cost = %v, want 0.15", cost)
	}
	if !metered {
		t.Error("metered should be true (all in-window lines had a cost)")
	}
}

// --- EstimateCost ---------------------------------------------------------

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEstimateCostRatesAndCacheMultipliers(t *testing.T) {
	// opus rate is {input:5, output:25} per 1M tokens.
	if got := EstimateCost("claude-opus-4-8", 1_000_000, 0, 0, 0); !approx(got, 5) {
		t.Errorf("input cost = %v, want 5", got)
	}
	if got := EstimateCost("claude-opus-4-8", 0, 1_000_000, 0, 0); !approx(got, 25) {
		t.Errorf("output cost = %v, want 25", got)
	}
	// cache read bills at 0.1× input rate.
	if got := EstimateCost("claude-opus-4-8", 0, 0, 1_000_000, 0); !approx(got, 0.5) {
		t.Errorf("cache-read cost = %v, want 0.5", got)
	}
	// cache write bills at 1.25× input rate.
	if got := EstimateCost("claude-opus-4-8", 0, 0, 0, 1_000_000); !approx(got, 6.25) {
		t.Errorf("cache-write cost = %v, want 6.25", got)
	}
}

func TestEstimateCostModelMatching(t *testing.T) {
	tests := []struct {
		model string
		want  float64 // cost of 1M input tokens
	}{
		{"claude-sonnet-4-6", 3},
		{"claude-haiku-4-5-20251001", 1},
		{"claude-fable-5", 10},
		{"claude-opus-4-8[1m]", 5},
		{"gpt-5.5", 0}, // unknown provider → unpriced
		{"", 0},        // empty model → 0, not a wrong number
		{"kimi-k2", 0}, // unknown → 0
	}
	for _, tc := range tests {
		if got := EstimateCost(tc.model, 1_000_000, 0, 0, 0); !approx(got, tc.want) {
			t.Errorf("EstimateCost(%q, 1M input) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestFormatPair(t *testing.T) {
	tests := []struct {
		tokens int
		cost   float64
		want   string
	}{
		{0, 0, "0 0"},
		{15234, 1.2, "15234 1.2"},
		{100, 1.25, "100 1.25"},
	}
	for _, tc := range tests {
		if got := FormatPair(tc.tokens, tc.cost); got != tc.want {
			t.Errorf("FormatPair(%d,%v) = %q, want %q", tc.tokens, tc.cost, got, tc.want)
		}
	}
}

// TestSessionTotalExcludesPriorRuns is the $101.55-banner regression guard: a
// resumed ticket's tokens.jsonl carries earlier sessions' spend, so the session
// summary must report only what THIS process appended while Total keeps the
// lifetime view.
func TestSessionTotalExcludesPriorRuns(t *testing.T) {
	dir := t.TempDir()
	prior := New(dir).WithClock(fixedClock(time.Date(2026, 7, 4, 20, 30, 0, 0, time.UTC)))
	prior.SetTicket("COD-702")
	prior.Append("build", Record{Output: 262_000, Turns: 225, CostUSD: ptr(57.88)})
	prior.Append("verify", Record{Output: 66_000, Turns: 72, CostUSD: ptr(11.19)})

	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 4, 22, 27, 0, 0, time.UTC)))
	s.SetTicket("COD-702")
	s.Append("status", Record{Output: 1_100, Turns: 7, CostUSD: ptr(1.08)})

	tk, cost, metered := s.SessionTotal("COD-702")
	if tk != 1_100 || cost != 1.08 || !metered {
		t.Errorf("SessionTotal = (%d, %v, %v), want only this process's (1100, 1.08, true)", tk, cost, metered)
	}
	ltk, lcost, _ := s.Total("COD-702")
	if ltk != 262_000+66_000+1_100 {
		t.Errorf("Total tokens = %d, want the lifetime sum %d", ltk, 262_000+66_000+1_100)
	}
	if lcost != 70.15 {
		t.Errorf("Total cost = %v, want lifetime 70.15", lcost)
	}
}

func TestSessionTotalUnknownTicketIsZero(t *testing.T) {
	s := New(t.TempDir())
	if tk, cost, metered := s.SessionTotal("COD-404"); tk != 0 || cost != 0 || !metered {
		t.Errorf("SessionTotal(unknown) = (%d, %v, %v), want (0, 0, true)", tk, cost, metered)
	}
}

// TestFlagIgnoresPriorRunSpend: resuming a ticket whose earlier sessions blew
// the rails must not re-flag those phases — only spend recorded by this process
// counts, so a cheap 27s reconcile run flags nothing.
func TestFlagIgnoresPriorRunSpend(t *testing.T) {
	dir := t.TempDir()
	prior := New(dir).WithClock(fixedClock(time.Date(2026, 7, 4, 20, 30, 0, 0, time.UTC)))
	prior.SetTicket("COD-702")
	prior.Append("build", Record{Output: 262_000, Turns: 225, CostUSD: ptr(57.88)})

	s := New(dir).WithClock(fixedClock(time.Date(2026, 7, 4, 22, 27, 0, 0, time.UTC)))
	s.SetTicket("COD-702")
	s.Append("status", Record{Output: 1_100, Turns: 7, CostUSD: ptr(1.08)})

	if got := s.Flag("COD-702"); len(got) != 0 {
		t.Errorf("Flag re-flagged prior-run spend: %+v", got)
	}
}
