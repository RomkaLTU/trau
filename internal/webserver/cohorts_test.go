package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// seedCohortCalls appends ledger rows carrying the cohort fields — config hash,
// duration, context — under the repo that owns runsDir, the shape the loop posts
// once it fingerprints its routing config.
func seedCohortCalls(t *testing.T, home, runsDir string, calls ...hubstore.TokenCall) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(runsDir))
	if err := testStoresAt(t, home).Tokens().Append(root, calls); err != nil {
		t.Fatalf("seed cohort calls: %v", err)
	}
}

func getCohorts(t *testing.T, ts *httptest.Server, repo, query string) ConfigCohortsResponse {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos/"+repo+"/metrics/config-cohorts"+query)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", res.StatusCode, body)
	}
	var out ConfigCohortsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode cohorts: %v (body %q)", err, body)
	}
	return out
}

func cohortByHash(cohorts []ConfigCohort) map[string]ConfigCohort {
	byHash := make(map[string]ConfigCohort, len(cohorts))
	for _, c := range cohorts {
		byHash[c.Hash] = c
	}
	return byHash
}

func phaseByName(phases []CohortPhase) map[string]CohortPhase {
	byName := make(map[string]CohortPhase, len(phases))
	for _, p := range phases {
		byName[p.Phase] = p
	}
	return byName
}

// seedTwoCohorts reports two routing fingerprints — the second dropping verify from
// xhigh to high — and seeds the ledger each ran, plus one pre-fingerprint call.
func seedTwoCohorts(t *testing.T, home, runsDir string, ts *httptest.Server) {
	t.Helper()
	postRouting(t, ts, "acme", routingInput{Hash: "cfg-a", Keys: map[string]string{
		"PROVIDER":     "claude",
		"PHASE_BUILD":  "claude:opus:xhigh",
		"PHASE_VERIFY": "claude:opus:xhigh",
	}})
	seedCohortCalls(t, home, runsDir,
		hubstore.TokenCall{
			Ticket: "COD-1", TS: "2026-07-01T10:00:00", Phase: "build", ConfigHash: "cfg-a",
			CostUSD: usd(1.00), DurationMS: 120000, Turns: 10, Context: 50000,
		},
		hubstore.TokenCall{
			Ticket: "COD-1", TS: "2026-07-01T10:30:00", Phase: "verify", ConfigHash: "cfg-a",
			CostUSD: usd(0.50), DurationMS: 60000, Turns: 5, Context: 30000,
		},
		hubstore.TokenCall{
			Ticket: "COD-1", TS: "2026-07-01T10:40:00", Phase: "repair1", ConfigHash: "cfg-a",
			CostUSD: usd(0.40), DurationMS: 40000, Turns: 4, Context: 20000,
		},
		hubstore.TokenCall{
			Ticket: "COD-1", TS: "2026-07-01T10:50:00", Phase: "verify-retry1", ConfigHash: "cfg-a",
			CostUSD: usd(0.60), DurationMS: 70000, Turns: 6, Context: 32000,
		},
		hubstore.TokenCall{
			Ticket: "COD-2", TS: "2026-07-02T09:00:00", Phase: "build", ConfigHash: "cfg-a",
			CostUSD: usd(2.00), DurationMS: 180000, Turns: 14, Context: 60000,
		},
		hubstore.TokenCall{
			Ticket: "COD-2", TS: "2026-07-02T09:30:00", Phase: "verify", ConfigHash: "cfg-a",
			CostUSD: usd(0.70), DurationMS: 50000, Turns: 5, Context: 28000,
		},
	)

	postRouting(t, ts, "acme", routingInput{Hash: "cfg-b", Keys: map[string]string{
		"PROVIDER":     "claude",
		"PHASE_BUILD":  "claude:opus:xhigh",
		"PHASE_VERIFY": "claude:opus:high",
	}})
	seedCohortCalls(t, home, runsDir,
		hubstore.TokenCall{
			Ticket: "COD-3", TS: "2026-07-10T09:00:00", Phase: "build", ConfigHash: "cfg-b",
			CostUSD: usd(1.00), DurationMS: 100000, Turns: 8, Context: 40000,
		},
		hubstore.TokenCall{
			Ticket: "COD-3", TS: "2026-07-10T09:20:00", Phase: "verify", ConfigHash: "cfg-b",
			CostUSD: usd(0.20), DurationMS: 30000, Turns: 3, Context: 15000,
		},
	)

	seedCohortCalls(t, home, runsDir, hubstore.TokenCall{
		Ticket: "COD-0", TS: "2026-06-01T09:00:00", Phase: "build",
		CostUSD: usd(0.30), DurationMS: 10000, Turns: 2, Context: 10000,
	})
}

// TestConfigCohortsAggregatesPerConfig is the acceptance contract: the ledger comes
// back as one entry per configuration, newest first, each carrying the routing
// values behind its hash, per-phase cost and speed with retries folded into the
// phase they retry, and the derived pipeline rates.
func TestConfigCohortsAggregatesPerConfig(t *testing.T) {
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	ts := ingestedServer(t, home)
	seedTwoCohorts(t, home, dirs["acme"], ts)

	page := getCohorts(t, ts, "acme", "")
	order := make([]string, len(page.Cohorts))
	for i, c := range page.Cohorts {
		order[i] = c.Hash
	}
	if len(order) != 3 || order[0] != "cfg-b" || order[1] != "cfg-a" || order[2] != unknownCohort {
		t.Fatalf("cohorts = %v, want cfg-b, cfg-a, unknown (newest first)", order)
	}

	a := cohortByHash(page.Cohorts)["cfg-a"]
	if a.Calls != 6 || a.Tickets != 2 {
		t.Errorf("cfg-a = %d calls over %d tickets, want 6 over 2", a.Calls, a.Tickets)
	}
	if a.CostUSD != 5.20 || a.CostPerTicket != 2.60 {
		t.Errorf("cfg-a cost = %v (%v per ticket), want 5.20 (2.60 per ticket)", a.CostUSD, a.CostPerTicket)
	}
	if a.FirstSeen != "2026-07-01T10:00:00" || a.LastSeen != "2026-07-02T09:30:00" {
		t.Errorf("cfg-a window = %s..%s, want its first and last call", a.FirstSeen, a.LastSeen)
	}
	if a.VerifyRetryRate != 0.5 || a.RepairRate != 0.5 {
		t.Errorf("cfg-a rates = retry %v repair %v, want 0.5 each (one of each over two verifies)", a.VerifyRetryRate, a.RepairRate)
	}
	if a.Routing["PHASE_VERIFY"] != "claude:opus:xhigh" {
		t.Errorf("cfg-a routing = %v, want the fingerprint it ran under", a.Routing)
	}

	phases := phaseByName(a.Phases)
	build := phases["build"]
	if build.Calls != 2 || build.CostUSD != 3.00 || build.AvgCostUSD != 1.50 {
		t.Errorf("cfg-a build = %+v, want 2 calls costing 3.00 (1.50 each)", build)
	}
	if build.AvgDurationMS != 150000 || build.AvgTurns != 12 || build.AvgContext != 55000 {
		t.Errorf("cfg-a build averages = %+v, want 150000ms, 12 turns, 55000 context", build)
	}
	if build.Provider != "claude" || build.Model != "opus" || build.Effort != "xhigh" {
		t.Errorf("cfg-a build route = %s/%s/%s, want claude/opus/xhigh", build.Provider, build.Model, build.Effort)
	}

	verify := phases["verify"]
	if verify.Calls != 3 || verify.CostUSD != 1.80 || verify.AvgCostUSD != 0.60 {
		t.Errorf("cfg-a verify = %+v, want its retry folded in: 3 calls costing 1.80", verify)
	}
	if verify.AvgDurationMS != 60000 || verify.AvgTurns != 5.33 {
		t.Errorf("cfg-a verify averages = %+v, want 60000ms and 5.33 turns", verify)
	}
	if repair := phases["repair"]; repair.Calls != 1 || repair.Effort != "" {
		t.Errorf("cfg-a repair = %+v, want the one call and no route (the fingerprint carries none)", repair)
	}

	b := cohortByHash(page.Cohorts)["cfg-b"]
	if b.VerifyRetryRate != 0 || b.RepairRate != 0 {
		t.Errorf("cfg-b rates = retry %v repair %v, want zero — nothing failed", b.VerifyRetryRate, b.RepairRate)
	}
	if got := phaseByName(b.Phases)["verify"].Effort; got != "high" {
		t.Errorf("cfg-b verify effort = %q, want high — the key the change moved", got)
	}
	if got := phaseByName(b.Phases)["build"].Effort; got != "xhigh" {
		t.Errorf("cfg-b build effort = %q, want xhigh carried through the change", got)
	}
}

// TestConfigCohortsWindowAndPhaseFilters covers the query params: the window keeps
// only the cohorts that ran inside it, and the phase filter narrows the breakdown
// while leaving each cohort's own totals whole.
func TestConfigCohortsWindowAndPhaseFilters(t *testing.T) {
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	ts := ingestedServer(t, home)
	seedTwoCohorts(t, home, dirs["acme"], ts)

	recent := getCohorts(t, ts, "acme", "?since=2026-07-05")
	if len(recent.Cohorts) != 1 || recent.Cohorts[0].Hash != "cfg-b" {
		t.Fatalf("cohorts since 2026-07-05 = %+v, want cfg-b alone", recent.Cohorts)
	}
	if recent.Since != "2026-07-05" {
		t.Errorf("since echo = %q, want the requested bound", recent.Since)
	}

	early := getCohorts(t, ts, "acme", "?since=2026-06-15&until=2026-07-02")
	if len(early.Cohorts) != 1 || early.Cohorts[0].Hash != "cfg-a" {
		t.Fatalf("cohorts in [2026-06-15, 2026-07-02] = %+v, want cfg-a alone", early.Cohorts)
	}
	if early.Cohorts[0].Calls != 6 {
		t.Errorf("cfg-a calls in window = %d, want all 6 — every call falls inside it", early.Cohorts[0].Calls)
	}

	only := getCohorts(t, ts, "acme", "?phase=verify")
	a := cohortByHash(only.Cohorts)["cfg-a"]
	if len(a.Phases) != 1 || a.Phases[0].Phase != "verify" {
		t.Fatalf("phases under ?phase=verify = %+v, want verify alone", a.Phases)
	}
	if a.Calls != 6 || a.RepairRate != 0.5 {
		t.Errorf("cfg-a totals under a phase filter = %d calls, repair rate %v, want the whole cohort (6, 0.5)", a.Calls, a.RepairRate)
	}
}

// TestConfigCohortsKeepsPreFingerprintHistory is the legacy contract: a ledger
// written before the routing fingerprint existed still reads, as the one unknown
// cohort with no routing values to show.
func TestConfigCohortsKeepsPreFingerprintHistory(t *testing.T) {
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	seedCohortCalls(t, home, dirs["acme"],
		hubstore.TokenCall{
			Ticket: "COD-1", TS: "2026-06-01T09:00:00", Phase: "build",
			CostUSD: usd(0.30), DurationMS: 10000, Turns: 2, Context: 10000,
		},
		hubstore.TokenCall{
			Ticket: "COD-2", TS: "2026-06-02T09:00:00", Phase: "verify",
			CostUSD: usd(0.10), DurationMS: 5000, Turns: 1, Context: 8000,
		},
	)
	ts := ingestedServer(t, home)

	page := getCohorts(t, ts, "acme", "")
	if len(page.Cohorts) != 1 || page.Cohorts[0].Hash != unknownCohort {
		t.Fatalf("cohorts = %+v, want the single unknown cohort", page.Cohorts)
	}
	unknown := page.Cohorts[0]
	if unknown.Calls != 2 || unknown.Tickets != 2 || unknown.CostUSD != 0.40 {
		t.Errorf("unknown cohort = %+v, want 2 calls over 2 tickets costing 0.40", unknown)
	}
	if unknown.Routing != nil {
		t.Errorf("unknown cohort routing = %v, want none — it never reported a fingerprint", unknown.Routing)
	}
	if len(unknown.Phases) != 2 {
		t.Errorf("unknown cohort phases = %+v, want build and verify", unknown.Phases)
	}
}

// TestConfigCohortsRejectsUnknownRepoMethodAndWindow keeps the endpoint's guards in
// line with the other repo-scoped reads, and refuses an unparseable window rather
// than silently answering over the whole ledger.
func TestConfigCohortsRejectsUnknownRepoMethodAndWindow(t *testing.T) {
	home := t.TempDir()
	seedRepos(t, home, "acme")
	ts := ingestedServer(t, home)

	res, _ := get(t, ts, APIPrefix+"/repos/nope/metrics/config-cohorts")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo status = %d, want 404", res.StatusCode)
	}

	post := postJSON(t, ts.URL+APIPrefix+"/repos/acme/metrics/config-cohorts", map[string]string{})
	_ = post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", post.StatusCode)
	}

	bad, _ := get(t, ts, APIPrefix+"/repos/acme/metrics/config-cohorts?since=last-week")
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("unparseable since status = %d, want 400", bad.StatusCode)
	}
}
