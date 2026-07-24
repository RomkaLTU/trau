package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testTokens(t *testing.T) *Tokens {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewTokens(db.SQL(), 0)
}

func usd(v float64) *float64 { return &v }

func appendCalls(t *testing.T, tk *Tokens, repo string, calls ...TokenCall) {
	t.Helper()
	for i := range calls {
		if calls[i].Total == 0 {
			calls[i].Total = calls[i].Input + calls[i].Output + calls[i].CacheRead + calls[i].CacheCreation
		}
	}
	if err := tk.Append(repo, calls); err != nil {
		t.Fatalf("append calls: %v", err)
	}
}

// TestTokensTotalAndPhaseTotals is the parity contract: a ticket's calls sum to the
// same ticket total and first-seen-ordered per-phase breakdown the file-era reader
// produced on the same inputs.
func TestTokensTotalAndPhaseTotals(t *testing.T) {
	tk := testTokens(t)
	const repo = "/repos/acme"
	appendCalls(t, tk, repo,
		TokenCall{Ticket: "COD-100", TS: "2026-07-12T10:00:00", Phase: "build", Input: 100, Output: 50, CacheRead: 10, CacheCreation: 5, CostUSD: usd(0.10), Turns: 3},
		TokenCall{Ticket: "COD-100", TS: "2026-07-12T10:01:00", Phase: "build", Input: 200, Output: 80, CacheRead: 20, CostUSD: usd(0.20), Turns: 4},
		TokenCall{Ticket: "COD-100", TS: "2026-07-12T10:02:00", Phase: "handoff", Input: 300, Output: 120, CostUSD: usd(0.05), Turns: 2},
		TokenCall{Ticket: "COD-100", TS: "2026-07-12T10:03:00", Phase: "verify", Input: 400, Output: 200, CacheRead: 50, CacheCreation: 10, CostUSD: usd(0.30), Turns: 5},
		TokenCall{Ticket: "COD-100", TS: "2026-07-12T10:04:00", Phase: "commit", Input: 50, Output: 20, CostUSD: usd(0.01), Turns: 1},
	)

	sp, err := tk.Total(repo, "COD-100")
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if sp.Tokens != 1615 || sp.Cost != 0.66 || !sp.Metered {
		t.Errorf("total = %+v, want tokens 1615 cost 0.66 metered", sp)
	}

	phases, err := tk.PhaseTotals(repo, "COD-100")
	if err != nil {
		t.Fatalf("PhaseTotals: %v", err)
	}
	wantOrder := []string{"build", "handoff", "verify", "commit"}
	if len(phases) != len(wantOrder) {
		t.Fatalf("phase rows = %d, want %d", len(phases), len(wantOrder))
	}
	for i, want := range wantOrder {
		if phases[i].Phase != want {
			t.Errorf("phase %d = %q, want %q (first-seen order)", i, phases[i].Phase, want)
		}
	}
	build := phases[0]
	want := PhaseTotal{
		Phase: "build", Input: 300, Output: 130, CacheRead: 30, CacheCreation: 5,
		Total: 465, Cost: 0.30, Turns: 7, Calls: 2, Metered: true,
	}
	if build != want {
		t.Errorf("build phase = %+v, want %+v", build, want)
	}
}

// TestTokensMeteredLowerBound covers the metered contract: a call with no per-call
// cost makes the ticket total (and its phase) a lower bound, not a measured total.
func TestTokensMeteredLowerBound(t *testing.T) {
	tk := testTokens(t)
	const repo = "/repos/acme"
	appendCalls(t, tk, repo,
		TokenCall{Ticket: "COD-1", TS: "2026-07-12T10:00:00", Phase: "build", Input: 100, Output: 50, CostUSD: usd(0.10)},
		TokenCall{Ticket: "COD-1", TS: "2026-07-12T10:01:00", Phase: "build", Input: 200, Output: 80},
	)
	sp, err := tk.Total(repo, "COD-1")
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if sp.Metered {
		t.Errorf("total = %+v, want unmetered (one call had no per-call cost)", sp)
	}
	if sp.Cost != 0.10 {
		t.Errorf("total cost = %v, want the 0.10 lower bound", sp.Cost)
	}
}

// TestTokensTotalMissingIsZero covers the no-calls case: a ticket the store never saw
// yields a zero, metered total rather than an error.
func TestTokensTotalMissingIsZero(t *testing.T) {
	tk := testTokens(t)
	sp, err := tk.Total("/repos/acme", "COD-404")
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if sp.Tokens != 0 || sp.Cost != 0 || !sp.Metered {
		t.Errorf("missing total = %+v, want (0, 0, true)", sp)
	}
}

// TestTokensDayTotalFiltersByDateAndRepo covers the budget day-cap read: DayTotal sums
// only the given repo's calls on the given local date, across every bucket, and never
// bleeds another repo's or another day's spend in.
func TestTokensDayTotalFiltersByDateAndRepo(t *testing.T) {
	tk := testTokens(t)
	const acme, beta = "/repos/acme", "/repos/beta"
	appendCalls(t, tk, acme,
		TokenCall{Ticket: "COD-1", TS: "2026-07-12T09:00:00", Phase: "build", Input: 100, Output: 100, CostUSD: usd(0.40)},
		TokenCall{Ticket: "_loop", TS: "2026-07-12T09:30:00", Phase: "pick", Input: 20, Output: 10, CostUSD: usd(0.02)},
		TokenCall{Ticket: "COD-2", TS: "2026-07-11T23:00:00", Phase: "build", Input: 999, Output: 999, CostUSD: usd(9.99)},
	)
	appendCalls(t, tk, beta,
		TokenCall{Ticket: "COD-9", TS: "2026-07-12T10:00:00", Phase: "build", Input: 500, Output: 500, CostUSD: usd(5.00)},
	)

	sp, err := tk.DayTotal(acme, "2026-07-12")
	if err != nil {
		t.Fatalf("DayTotal: %v", err)
	}
	if sp.Tokens != 230 || sp.Cost != 0.42 {
		t.Errorf("acme day total = %+v, want tokens 230 cost 0.42 (both buckets, not yesterday, not beta)", sp)
	}
}

// TestTokensCostCellsWindow covers the costs page's aggregation: cells group by
// (repo, date, provider, model, phase) and the [from, to] window filters out spend
// outside it.
func TestTokensCostCellsWindow(t *testing.T) {
	tk := testTokens(t)
	const repo = "/repos/acme"
	appendCalls(t, tk, repo,
		TokenCall{Ticket: "COD-1", TS: "2026-07-12T10:00:00", Phase: "build", Provider: "claude", Model: "opus", Input: 300, Output: 200, CostUSD: usd(0.50)},
		TokenCall{Ticket: "COD-1", TS: "2026-07-12T10:01:00", Phase: "build", Provider: "claude", Model: "opus", Input: 100, Output: 100, CostUSD: usd(0.25)},
		TokenCall{Ticket: "COD-OLD", TS: "2026-06-01T10:00:00", Phase: "build", Provider: "claude", Model: "opus", Input: 9000, Output: 900, CostUSD: usd(9.99)},
	)
	cells, err := tk.CostCells("2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatalf("CostCells: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("cells = %d, want 1 (the two in-window build calls fold into one cell)", len(cells))
	}
	c := cells[0]
	if c.Tokens != 700 || c.Cost != 0.75 || c.Phase != "build" || c.Provider != "claude" || c.Model != "opus" {
		t.Errorf("cell = %+v, want the folded in-window build spend", c)
	}
}

// TestTokensAnomaliesRoundTrip covers recording and reading anomalies: they come back
// per ticket in record order, RepoAnomalies locates each to its ticket, and an empty
// record leaves a prior run's anomalies untouched.
func TestTokensAnomaliesRoundTrip(t *testing.T) {
	tk := testTokens(t)
	const repo = "/repos/acme"
	first := []Anomaly{
		{TS: "2026-07-12T10:00:00", Phase: "cleanup", Output: 120_000, Turns: 8, Cost: 6.5, Reasons: []string{"cost $6.50 > $3.00", "output 120000 > 25000"}},
	}
	if err := tk.RecordAnomalies(repo, "COD-9", first); err != nil {
		t.Fatalf("RecordAnomalies: %v", err)
	}

	got, err := tk.Anomalies(repo, "COD-9")
	if err != nil {
		t.Fatalf("Anomalies: %v", err)
	}
	if len(got) != 1 || got[0].Phase != "cleanup" || got[0].Cost != 6.5 || len(got[0].Reasons) != 2 {
		t.Fatalf("anomalies = %+v, want the single cleanup trip with two reasons", got)
	}

	// An empty record is a no-op — it must not clear the prior anomalies.
	if err := tk.RecordAnomalies(repo, "COD-9", nil); err != nil {
		t.Fatalf("RecordAnomalies empty: %v", err)
	}
	if got, _ := tk.Anomalies(repo, "COD-9"); len(got) != 1 {
		t.Errorf("after empty record anomalies = %d, want the prior 1 untouched", len(got))
	}

	// A re-record replaces the ticket's anomalies rather than appending.
	if err := tk.RecordAnomalies(repo, "COD-9", []Anomaly{{Phase: "verify", Cost: 4.0, Reasons: []string{"cost $4.00 > $3.00"}}}); err != nil {
		t.Fatalf("RecordAnomalies replace: %v", err)
	}
	got, _ = tk.Anomalies(repo, "COD-9")
	if len(got) != 1 || got[0].Phase != "verify" {
		t.Errorf("after replace anomalies = %+v, want just the verify trip", got)
	}

	if err := tk.RecordAnomalies(repo, "COD-10", []Anomaly{{Phase: "build", Cost: 3.5, Reasons: []string{"cost $3.50 > $3.00"}}}); err != nil {
		t.Fatalf("RecordAnomalies COD-10: %v", err)
	}
	repoAnoms, err := tk.RepoAnomalies(repo)
	if err != nil {
		t.Fatalf("RepoAnomalies: %v", err)
	}
	if len(repoAnoms) != 2 {
		t.Fatalf("repo anomalies = %d, want 2 across both tickets", len(repoAnoms))
	}
	byTicket := map[string]string{}
	for _, a := range repoAnoms {
		byTicket[a.Ticket] = a.Phase
	}
	if byTicket["COD-9"] != "verify" || byTicket["COD-10"] != "build" {
		t.Errorf("repo anomalies located wrong: %+v", byTicket)
	}
}

func TestTokensPruneKeepsRecentPerRepo(t *testing.T) {
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tk := NewTokens(db.SQL(), 2)

	const repo = "/repos/acme"
	for _, id := range []string{"COD-1", "COD-2", "COD-3", "COD-4"} {
		appendCalls(t, tk, repo, TokenCall{Ticket: id, Phase: "build", Input: 100, Output: 50})
	}
	appendCalls(t, tk, "/repos/other", TokenCall{Ticket: "OTH-1", Phase: "build", Input: 10, Output: 5})

	for _, id := range []string{"COD-1", "COD-2", "COD-3"} {
		if err := tk.RecordAnomalies(repo, id, []Anomaly{{Phase: "build", Output: 999, Reasons: []string{"spike"}}}); err != nil {
			t.Fatalf("RecordAnomalies %s: %v", id, err)
		}
	}

	if err := tk.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	for _, id := range []string{"COD-1", "COD-2"} {
		if sp, _ := tk.Total(repo, id); sp.Tokens != 0 {
			t.Fatalf("ticket %s survived prune with %d tokens, want 0", id, sp.Tokens)
		}
	}
	for _, id := range []string{"COD-3", "COD-4"} {
		if sp, _ := tk.Total(repo, id); sp.Tokens == 0 {
			t.Fatalf("ticket %s pruned, want it kept", id)
		}
	}
	if sp, _ := tk.Total("/repos/other", "OTH-1"); sp.Tokens == 0 {
		t.Fatalf("other repo under the window pruned, want kept")
	}
	if an, _ := tk.RepoAnomalies(repo); len(an) != 2 {
		t.Fatalf("anomalies after prune = %d, want 2", len(an))
	}
}

// TestTokensAppendRecordsRouting keeps the three columns a config cohort is
// grouped by on the write path. A call reporting none of them lands on the
// defaults historical rows carry.
func TestTokensAppendRecordsRouting(t *testing.T) {
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tk := NewTokens(db.SQL(), 0)

	const repo = "/repos/acme"
	appendCalls(t, tk, repo,
		TokenCall{Ticket: "COD-1", Phase: "verify", Input: 10, Output: 5, Effort: "high", DurationMS: 42_000, ConfigHash: "9f1c2a"},
		TokenCall{Ticket: "COD-1", Phase: "commit", Input: 1, Output: 1},
	)

	rows, err := db.SQL().Query(
		`SELECT phase, effort, duration_ms, config_hash FROM token_calls WHERE repo = ? AND ticket = ? ORDER BY id`,
		repo, "COD-1",
	)
	if err != nil {
		t.Fatalf("query token calls: %v", err)
	}
	defer func() { _ = rows.Close() }()

	type row struct {
		phase, effort, hash string
		durationMS          int
	}
	got := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.phase, &r.effort, &r.durationMS, &r.hash); err != nil {
			t.Fatalf("scan token call: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate token calls: %v", err)
	}
	want := []row{
		{phase: "verify", effort: "high", durationMS: 42_000, hash: "9f1c2a"},
		{phase: "commit"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestSkillCallsWindow is the activation-evidence contract: calls inside the
// window come back oldest first with their skills decoded, older ones are
// dropped, and a call whose provider reported nothing yields no names rather
// than a decode error.
func TestSkillCallsWindow(t *testing.T) {
	tk := testTokens(t)
	const repo = "/repos/acme"
	appendCalls(t, tk, repo,
		TokenCall{Ticket: "COD-1", TS: "2026-05-01T09:00:00", Phase: "build", Provider: "claude", Skills: `["old-skill"]`},
		TokenCall{Ticket: "COD-2", TS: "2026-07-10T09:00:00", Phase: "build", Provider: "claude", Skills: `["golang-cli","web-feature"]`},
		TokenCall{Ticket: "COD-2", TS: "2026-07-11T09:00:00", Phase: "verify", Provider: "codex", Skills: ""},
	)
	appendCalls(t, tk, "/repos/other", TokenCall{Ticket: "X-1", TS: "2026-07-10T09:00:00", Provider: "claude", Skills: `["stray"]`})

	calls, err := tk.SkillCalls(repo, "2026-07-01")
	if err != nil {
		t.Fatalf("skill calls: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2 (%+v)", len(calls), calls)
	}
	if got := calls[0]; got.Ticket != "COD-2" || got.Phase != "build" || len(got.Skills) != 2 || got.Skills[0] != "golang-cli" {
		t.Fatalf("first call = %+v", got)
	}
	if got := calls[1]; got.Provider != "codex" || len(got.Skills) != 0 {
		t.Fatalf("second call = %+v, want codex with no names", got)
	}
}
