package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// seedDayTokens appends a run's calls dated on a specific day through the real
// Sink, so the rollup reads the exact on-disk log shape the pipeline writes.
func seedDayTokens(t *testing.T, runsDir, id string, day time.Time, calls []phaseCall) {
	t.Helper()
	sink := tokens.New(runsDir).WithClock(func() time.Time { return day })
	sink.SetTicket(id)
	for _, c := range calls {
		sink.Append(c.phase, c.rec)
	}
}

func getCosts(t *testing.T, ts *httptest.Server, query string) CostsResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/costs" + query)
	if err != nil {
		t.Fatalf("GET costs: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out CostsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode costs: %v", err)
	}
	return out
}

func dailyByDate(daily []DailyCost) map[string]DailyCost {
	m := make(map[string]DailyCost, len(daily))
	for _, d := range daily {
		m[d.Date] = d
	}
	return m
}

func repoCostByName(repos []RepoCost) map[string]RepoCost {
	m := make(map[string]RepoCost, len(repos))
	for _, r := range repos {
		m[r.Repo] = r
	}
	return m
}

func phaseSpendByName(phases []PhaseSpend) map[string]PhaseSpend {
	m := make(map[string]PhaseSpend, len(phases))
	for _, p := range phases {
		m[p.Phase] = p
	}
	return m
}

// TestCostsRollupAcrossReposAndDays is the fixture-driven contract test: two
// repos' runs spread over three days fold into exact daily, per-repo, and
// per-phase rollups, a continuous zero-filled date axis, and correct totals —
// while a call outside the window is excluded entirely.
func TestCostsRollupAcrossReposAndDays(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme", "beta")

	now := time.Now()
	day := func(n int) time.Time { return now.AddDate(0, 0, n) }
	fmtDay := func(n int) string { return day(n).Format(dateLayout) }

	seedDayTokens(t, dirs["acme"], "COD-1", day(0), []phaseCall{
		{"build", tokens.Record{Input: 300, Output: 200, CostUSD: usd(0.50)}},
		{"verify", tokens.Record{Input: 100, Output: 100, CostUSD: usd(0.20)}},
	})
	seedDayTokens(t, dirs["acme"], "COD-2", day(-1), []phaseCall{
		{"build", tokens.Record{Input: 60, Output: 40, CostUSD: usd(0.10)}},
	})
	seedDayTokens(t, dirs["beta"], "COD-3", day(0), []phaseCall{
		{"build", tokens.Record{Input: 200, Output: 100, CostUSD: usd(0.30)}},
	})
	seedDayTokens(t, dirs["beta"], "COD-3", day(-2), []phaseCall{
		{"commit", tokens.Record{Input: 30, Output: 20, CostUSD: usd(0.05)}},
	})
	// Well outside a 7-day window — must not appear anywhere.
	seedDayTokens(t, dirs["acme"], "COD-OLD", day(-40), []phaseCall{
		{"build", tokens.Record{Input: 9000, Output: 999, CostUSD: usd(9.99)}},
	})

	ts := instancesServer(t, home)
	c := getCosts(t, ts, "?days=7")

	if c.WindowDays != 7 || c.From != fmtDay(-6) || c.To != fmtDay(0) {
		t.Errorf("window = %d days [%s, %s], want 7 days [%s, %s]", c.WindowDays, c.From, c.To, fmtDay(-6), fmtDay(0))
	}
	if c.Totals.Tokens != 1150 || c.Totals.CostUSD != 1.15 || !c.Totals.Metered {
		t.Errorf("totals = %+v, want tokens 1150 cost 1.15 metered", c.Totals)
	}

	if len(c.Daily) != 7 {
		t.Fatalf("daily series = %d entries, want 7 (zero-filled window)", len(c.Daily))
	}
	byDay := dailyByDate(c.Daily)
	if d := byDay[fmtDay(0)]; d.Tokens != 1000 || d.CostUSD != 1.00 {
		t.Errorf("today = %+v, want tokens 1000 cost 1.00 (both repos)", d)
	}
	if d := byDay[fmtDay(-1)]; d.Tokens != 100 || d.CostUSD != 0.10 {
		t.Errorf("yesterday = %+v, want tokens 100 cost 0.10", d)
	}
	if d := byDay[fmtDay(-2)]; d.Tokens != 50 || d.CostUSD != 0.05 {
		t.Errorf("two days ago = %+v, want tokens 50 cost 0.05", d)
	}
	if d := byDay[fmtDay(-3)]; d.Tokens != 0 || d.CostUSD != 0 {
		t.Errorf("empty day = %+v, want a zero-filled entry", d)
	}

	byRepo := repoCostByName(c.Repos)
	if len(c.Repos) != 2 {
		t.Fatalf("repo breakdown = %d rows, want 2", len(c.Repos))
	}
	if r := byRepo["acme"]; r.Tokens != 800 || r.CostUSD != 0.80 {
		t.Errorf("acme = %+v, want tokens 800 cost 0.80", r)
	}
	if r := byRepo["beta"]; r.Tokens != 350 || r.CostUSD != 0.35 {
		t.Errorf("beta = %+v, want tokens 350 cost 0.35", r)
	}

	byPhase := phaseSpendByName(c.Phases)
	if p := byPhase["build"]; p.Tokens != 900 || p.CostUSD != 0.90 {
		t.Errorf("build phase = %+v, want tokens 900 cost 0.90", p)
	}
	if p := byPhase["verify"]; p.Tokens != 200 || p.CostUSD != 0.20 {
		t.Errorf("verify phase = %+v, want tokens 200 cost 0.20", p)
	}
	if p := byPhase["commit"]; p.Tokens != 50 || p.CostUSD != 0.05 {
		t.Errorf("commit phase = %+v, want tokens 50 cost 0.05", p)
	}
	if c.Phases[0].Phase != "build" {
		t.Errorf("phases ordered %q first, want build (costliest first)", c.Phases[0].Phase)
	}
}

// TestCostsWindowSelectable confirms the window is a real filter: widening it to
// 60 days pulls in a run that a 7-day window excludes.
func TestCostsWindowSelectable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	now := time.Now()

	seedDayTokens(t, dirs["acme"], "COD-1", now, []phaseCall{
		{"build", tokens.Record{Input: 100, Output: 100, CostUSD: usd(0.20)}},
	})
	seedDayTokens(t, dirs["acme"], "COD-OLD", now.AddDate(0, 0, -40), []phaseCall{
		{"build", tokens.Record{Input: 500, Output: 500, CostUSD: usd(1.00)}},
	})

	ts := instancesServer(t, home)

	narrow := getCosts(t, ts, "?days=7")
	if narrow.Totals.CostUSD != 0.20 {
		t.Errorf("7-day total = %v, want 0.20 (old run excluded)", narrow.Totals.CostUSD)
	}
	wide := getCosts(t, ts, "?days=60")
	if wide.Totals.CostUSD != 1.20 {
		t.Errorf("60-day total = %v, want 1.20 (old run included)", wide.Totals.CostUSD)
	}
	if wide.WindowDays != 60 || len(wide.Daily) != 60 {
		t.Errorf("wide window = %d days, %d daily entries, want 60/60", wide.WindowDays, len(wide.Daily))
	}
}

// TestCostsBudgetCapsAsContext covers the budget-as-context contract: a repo's
// configured daily cap rides its breakdown row, a repo with a cap but no spend
// still appears (headroom), and the machine-wide budget sums the configured caps.
func TestCostsBudgetCapsAsContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAX_DAILY_USD", "")
	t.Setenv("TRAU_MAX_DAILY_USD", "")
	home := t.TempDir()
	dirs := seedRepos(t, home, "capped", "quiet")

	writeRepoConfig(t, home, "capped", "MAX_DAILY_USD=5\n")
	writeRepoConfig(t, home, "quiet", "MAX_DAILY_USD=2\n")

	seedDayTokens(t, dirs["capped"], "COD-1", time.Now(), []phaseCall{
		{"build", tokens.Record{Input: 100, Output: 100, CostUSD: usd(0.40)}},
	})

	ts := instancesServer(t, home)
	c := getCosts(t, ts, "?days=7")

	byRepo := repoCostByName(c.Repos)
	if r := byRepo["capped"]; r.DailyBudgetUSD != 5 || r.CostUSD != 0.40 {
		t.Errorf("capped repo = %+v, want cost 0.40 against a $5 daily cap", r)
	}
	quiet, ok := byRepo["quiet"]
	if !ok {
		t.Fatalf("quiet repo missing; a repo with a cap but no spend should still show its headroom")
	}
	if quiet.CostUSD != 0 || quiet.DailyBudgetUSD != 2 {
		t.Errorf("quiet repo = %+v, want zero spend against a $2 daily cap", quiet)
	}
	if c.Budget.DailyUSD != 7 {
		t.Errorf("machine-wide daily budget = %v, want 7 (sum of caps)", c.Budget.DailyUSD)
	}
}

// TestCostsSurfacesAnomaliesAcrossRepos covers the anomalies list: a flagged
// anomaly is visible on the Costs page, located to the repo and ticket that
// produced it.
func TestCostsSurfacesAnomaliesAcrossRepos(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	seedCheckpoint(t, dirs["acme"], "COD-9", map[string]string{"PHASE": state.Building})

	sink := tokens.New(dirs["acme"]).WithClock(func() time.Time { return time.Now() })
	sink.SetTicket("COD-9")
	sink.Append("cleanup", tokens.Record{Output: 120_000, Turns: 8, CostUSD: usd(6.50)})
	sink.Flag("COD-9")

	ts := instancesServer(t, home)
	c := getCosts(t, ts, "?days=7")

	if len(c.Anomalies) != 1 {
		t.Fatalf("anomalies = %d, want 1 flagged trip", len(c.Anomalies))
	}
	a := c.Anomalies[0]
	if a.Repo != "acme" || a.Ticket != "COD-9" || a.Phase != "cleanup" || a.CostUSD != 6.5 {
		t.Errorf("anomaly = %+v, want acme/COD-9 cleanup at $6.50", a)
	}
	if len(a.Reasons) == 0 {
		t.Error("anomaly carries no reasons, want the tripped thresholds")
	}
}

// TestCostsRejectsNonGET keeps the resource read-only.
func TestCostsRejectsNonGET(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Post(ts.URL+APIPrefix+"/costs", "application/json", nil)
	if err != nil {
		t.Fatalf("POST costs: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}

// writeRepoConfig writes a <repo>/.trau.ini for a known repo, so a budget cap
// resolves through the same layered config the loop reads.
func writeRepoConfig(t *testing.T, home, name, body string) {
	t.Helper()
	known, err := testRegistrationsAt(t, home).Known()
	if err != nil {
		t.Fatalf("read known repos: %v", err)
	}
	for _, repo := range known {
		if repo.Name != name {
			continue
		}
		if err := os.MkdirAll(repo.Root, 0o755); err != nil {
			t.Fatalf("mkdir %s root: %v", name, err)
		}
		if err := os.WriteFile(config.ProjectConfigPath(repo.Root), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s config: %v", name, err)
		}
		return
	}
	t.Fatalf("repo %q not found in known repos", name)
}
