package hubstore

import "testing"

func cohortByHash(cohorts []CohortTotal) map[string]CohortTotal {
	byHash := make(map[string]CohortTotal, len(cohorts))
	for _, c := range cohorts {
		byHash[c.Hash] = c
	}
	return byHash
}

func seedCohortLedger(t *testing.T, tk *Tokens, repo string) {
	t.Helper()
	appendCalls(t, tk, repo,
		TokenCall{Ticket: "COD-1", TS: "2026-07-01T10:00:00", Phase: "build", ConfigHash: "cfg-a", CostUSD: usd(1.00), DurationMS: 120000, Turns: 10, Context: 50000},
		TokenCall{Ticket: "COD-1", TS: "2026-07-01T10:30:00", Phase: "verify", ConfigHash: "cfg-a", CostUSD: usd(0.50), DurationMS: 60000, Turns: 5, Context: 30000},
		TokenCall{Ticket: "COD-1", TS: "2026-07-01T10:40:00", Phase: "repair1", ConfigHash: "cfg-a", CostUSD: usd(0.40), DurationMS: 40000, Turns: 4, Context: 20000},
		TokenCall{Ticket: "COD-1", TS: "2026-07-01T10:50:00", Phase: "verify-retry1", ConfigHash: "cfg-a", CostUSD: usd(0.60), DurationMS: 70000, Turns: 6, Context: 32000},
		TokenCall{Ticket: "COD-2", TS: "2026-07-02T09:00:00", Phase: "build", ConfigHash: "cfg-a", CostUSD: usd(2.00), DurationMS: 180000, Turns: 14, Context: 60000},
		TokenCall{Ticket: "COD-3", TS: "2026-07-10T09:00:00", Phase: "build", ConfigHash: "cfg-b", CostUSD: usd(1.00), DurationMS: 100000, Turns: 8, Context: 40000},
		TokenCall{Ticket: "COD-3", TS: "2026-07-10T09:20:00", Phase: "verify", ConfigHash: "cfg-b", DurationMS: 30000, Turns: 3, Context: 15000},
		TokenCall{Ticket: "COD-0", TS: "2026-06-01T09:00:00", Phase: "build", CostUSD: usd(0.30), DurationMS: 10000, Turns: 2, Context: 10000},
	)
}

// TestConfigCohortTotalsGroupsByHash is the ledger contract behind the metrics
// endpoint: calls fold into one row per config_hash, newest cohort first, with the
// verify/retry/repair counters the derived rates divide — and the calls that predate
// the fingerprint keep their own empty-hash cohort instead of being dropped.
func TestConfigCohortTotalsGroupsByHash(t *testing.T) {
	tk := testTokens(t)
	const repo = "/repos/acme"
	seedCohortLedger(t, tk, repo)

	cohorts, err := tk.ConfigCohortTotals(repo, CohortFilter{})
	if err != nil {
		t.Fatalf("ConfigCohortTotals: %v", err)
	}
	order := make([]string, len(cohorts))
	for i, c := range cohorts {
		order[i] = c.Hash
	}
	want := []string{"cfg-b", "cfg-a", ""}
	if len(order) != len(want) {
		t.Fatalf("cohorts = %v, want %v (newest first)", order, want)
	}
	for i, hash := range want {
		if order[i] != hash {
			t.Fatalf("cohorts = %v, want %v (newest first)", order, want)
		}
	}

	byHash := cohortByHash(cohorts)
	a := byHash["cfg-a"]
	if a.Calls != 5 || a.Tickets != 2 {
		t.Errorf("cfg-a = %d calls over %d tickets, want 5 over 2", a.Calls, a.Tickets)
	}
	if a.Cost != 4.50 || !a.Metered {
		t.Errorf("cfg-a cost = %v metered %v, want 4.50 metered", a.Cost, a.Metered)
	}
	if a.FirstTS != "2026-07-01T10:00:00" || a.LastTS != "2026-07-02T09:00:00" {
		t.Errorf("cfg-a window = %s..%s, want the first and last call", a.FirstTS, a.LastTS)
	}
	if a.VerifyCalls != 1 || a.RetryCalls != 1 || a.RepairCalls != 1 {
		t.Errorf("cfg-a counters = verify %d retry %d repair %d, want 1/1/1", a.VerifyCalls, a.RetryCalls, a.RepairCalls)
	}

	b := byHash["cfg-b"]
	if b.RetryCalls != 0 || b.RepairCalls != 0 || b.VerifyCalls != 1 {
		t.Errorf("cfg-b counters = verify %d retry %d repair %d, want 1/0/0", b.VerifyCalls, b.RetryCalls, b.RepairCalls)
	}
	if b.Metered {
		t.Error("cfg-b reported metered, want a lower bound: its verify call recorded no cost")
	}

	unknown := byHash[""]
	if unknown.Calls != 1 || unknown.Tickets != 1 {
		t.Errorf("unknown cohort = %d calls over %d tickets, want 1 over 1", unknown.Calls, unknown.Tickets)
	}
}

// TestConfigCohortWindowAndPhaseCells covers the window bounds and the per-phase
// cells: a window keeps only the cohorts whose calls fall inside it, and every raw
// phase label lands in its own cell with raw sums.
func TestConfigCohortWindowAndPhaseCells(t *testing.T) {
	tk := testTokens(t)
	const repo = "/repos/acme"
	seedCohortLedger(t, tk, repo)

	recent, err := tk.ConfigCohortTotals(repo, CohortFilter{Since: "2026-07-05"})
	if err != nil {
		t.Fatalf("ConfigCohortTotals since: %v", err)
	}
	if len(recent) != 1 || recent[0].Hash != "cfg-b" {
		t.Fatalf("cohorts since 2026-07-05 = %+v, want cfg-b alone", recent)
	}

	early, err := tk.ConfigCohortTotals(repo, CohortFilter{Until: "2026-07-02"})
	if err != nil {
		t.Fatalf("ConfigCohortTotals until: %v", err)
	}
	if len(early) != 2 {
		t.Fatalf("cohorts until 2026-07-02 = %d, want cfg-a and the unknown cohort", len(early))
	}

	cells, err := tk.ConfigCohortPhases(repo, CohortFilter{Since: "2026-07-01", Until: "2026-07-01"})
	if err != nil {
		t.Fatalf("ConfigCohortPhases: %v", err)
	}
	byPhase := make(map[string]CohortPhaseCell, len(cells))
	for _, c := range cells {
		if c.Hash != "cfg-a" {
			t.Fatalf("cell outside the window: %+v", c)
		}
		byPhase[c.Phase] = c
	}
	if len(byPhase) != 4 {
		t.Fatalf("phase cells = %v, want build, verify, repair1, and verify-retry1 kept apart", byPhase)
	}
	verify := byPhase["verify"]
	if verify.Calls != 1 || verify.DurationMS != 60000 || verify.Turns != 5 || verify.Context != 30000 {
		t.Errorf("verify cell = %+v, want the raw per-call sums", verify)
	}
}
