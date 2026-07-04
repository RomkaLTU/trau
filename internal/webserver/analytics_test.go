package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/tokens"
)

func getTimeseries(t *testing.T, ts *httptest.Server, query string) TimeseriesResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/costs/timeseries" + query)
	if err != nil {
		t.Fatalf("GET timeseries: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out TimeseriesResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode timeseries: %v", err)
	}
	return out
}

func seriesByKey(series []TimeseriesGroup) map[string]TimeseriesGroup {
	m := make(map[string]TimeseriesGroup, len(series))
	for _, s := range series {
		m[s.Key] = s
	}
	return m
}

func pointByDate(points []TimeseriesPoint) map[string]TimeseriesPoint {
	m := make(map[string]TimeseriesPoint, len(points))
	for _, p := range points {
		m[p.Date] = p
	}
	return m
}

// seedAnalyticsFixture lays down two repos of spend across two providers and three
// days, provider attribution deriving from the recorded model — the historical
// shape the analytics reader must fold. It returns the day formatter for windows.
func seedAnalyticsFixture(t *testing.T, home string) (dirs map[string]string, fmtDay func(int) string) {
	t.Helper()
	dirs = seedRepos(t, home, "acme", "beta")
	now := time.Now()
	day := func(n int) time.Time { return now.AddDate(0, 0, n) }
	fmtDay = func(n int) string { return day(n).Format(dateLayout) }

	seedDayTokens(t, dirs["acme"], "COD-1", day(0), []phaseCall{
		{"build", tokens.Record{Input: 300, Output: 200, CostUSD: usd(0.50), Model: "claude-opus-4-8"}},
		{"verify", tokens.Record{Input: 100, Output: 100, CostUSD: usd(0.20), Model: "gpt-5.4"}},
	})
	seedDayTokens(t, dirs["acme"], "COD-2", day(-1), []phaseCall{
		{"build", tokens.Record{Input: 60, Output: 40, CostUSD: usd(0.10), Model: "claude-sonnet-5"}},
	})
	seedDayTokens(t, dirs["beta"], "COD-3", day(0), []phaseCall{
		{"build", tokens.Record{Input: 200, Output: 100, CostUSD: usd(0.30), Model: "kimi-k2"}},
	})
	seedDayTokens(t, dirs["beta"], "COD-3", day(-2), []phaseCall{
		{"commit", tokens.Record{Input: 30, Output: 20, CostUSD: usd(0.05), Model: "claude-haiku-4-5"}},
	})
	// Outside a 7-day window — must never contribute.
	seedDayTokens(t, dirs["acme"], "COD-OLD", day(-40), []phaseCall{
		{"build", tokens.Record{Input: 9000, Output: 999, CostUSD: usd(9.99), Model: "claude-opus-4-8"}},
	})
	return dirs, fmtDay
}

// TestTimeseriesGroupsByProvider is the fixture-driven contract test: spend across
// two repos and three providers folds into one series per provider, each derived
// from the recorded model, with correct window totals, a continuous zero-filled
// axis, cost-ordered series, and facets covering every dimension in the window.
func TestTimeseriesGroupsByProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	_, fmtDay := seedAnalyticsFixture(t, home)

	ts := instancesServer(t, home)
	r := getTimeseries(t, ts, "?days=7")

	if r.GroupBy != "provider" || r.Days != 7 || r.From != fmtDay(-6) || r.To != fmtDay(0) {
		t.Fatalf("window = group %q %d days [%s, %s], want provider 7 [%s, %s]", r.GroupBy, r.Days, r.From, r.To, fmtDay(-6), fmtDay(0))
	}
	if r.Totals.Tokens != 1150 || r.Totals.CostUSD != 1.15 || !r.Totals.Metered {
		t.Errorf("totals = %+v, want tokens 1150 cost 1.15 metered", r.Totals)
	}

	if len(r.Series) != 3 {
		t.Fatalf("series = %d, want 3 (claude, codex, kimi)", len(r.Series))
	}
	if r.Series[0].Key != "claude" {
		t.Errorf("first series = %q, want claude (costliest)", r.Series[0].Key)
	}
	byKey := seriesByKey(r.Series)
	if g := byKey["claude"]; g.Tokens != 650 || g.CostUSD != 0.65 {
		t.Errorf("claude series = %+v, want tokens 650 cost 0.65", g)
	}
	if g := byKey["codex"]; g.Tokens != 200 || g.CostUSD != 0.20 {
		t.Errorf("codex series = %+v, want tokens 200 cost 0.20", g)
	}
	if g := byKey["kimi"]; g.Tokens != 300 || g.CostUSD != 0.30 {
		t.Errorf("kimi series = %+v, want tokens 300 cost 0.30", g)
	}

	claude := byKey["claude"]
	if len(claude.Points) != 7 {
		t.Fatalf("claude points = %d, want 7 (zero-filled window)", len(claude.Points))
	}
	pts := pointByDate(claude.Points)
	if p := pts[fmtDay(0)]; p.Tokens != 500 || p.CostUSD != 0.50 {
		t.Errorf("claude today = %+v, want tokens 500 cost 0.50 (opus build)", p)
	}
	if p := pts[fmtDay(-1)]; p.Tokens != 100 || p.CostUSD != 0.10 {
		t.Errorf("claude yesterday = %+v, want tokens 100 cost 0.10 (sonnet build)", p)
	}
	if p := pts[fmtDay(-3)]; p.Tokens != 0 || p.CostUSD != 0 {
		t.Errorf("claude empty day = %+v, want a zero-filled point", p)
	}

	f := r.Facets
	if !eqStrings(f.Repos, []string{"acme", "beta"}) {
		t.Errorf("repo facets = %v, want [acme beta]", f.Repos)
	}
	if !eqStrings(f.Providers, []string{"claude", "codex", "kimi"}) {
		t.Errorf("provider facets = %v, want [claude codex kimi]", f.Providers)
	}
	if !eqStrings(f.Phases, []string{"build", "commit", "verify"}) {
		t.Errorf("phase facets = %v, want [build commit verify]", f.Phases)
	}
	if !eqStrings(f.Models, []string{"claude-haiku-4-5", "claude-opus-4-8", "claude-sonnet-5", "gpt-5.4", "kimi-k2"}) {
		t.Errorf("model facets = %v, want the five recorded models", f.Models)
	}
}

// TestTimeseriesFilters confirms the dimensions are real filters: a provider filter
// keeps only that provider's spend, a phase filter cuts across providers, and the
// facets still list every dimension in the window so the controls stay populated.
func TestTimeseriesFilters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	seedAnalyticsFixture(t, home)
	ts := instancesServer(t, home)

	only := getTimeseries(t, ts, "?days=7&provider=claude")
	if len(only.Series) != 1 || only.Series[0].Key != "claude" {
		t.Fatalf("provider=claude series = %+v, want a single claude series", only.Series)
	}
	if only.Totals.CostUSD != 0.65 || only.Totals.Tokens != 650 {
		t.Errorf("provider=claude totals = %+v, want tokens 650 cost 0.65", only.Totals)
	}
	if len(only.Facets.Providers) != 3 {
		t.Errorf("provider facets under filter = %v, want all three still listed", only.Facets.Providers)
	}

	builds := getTimeseries(t, ts, "?days=7&phase=build")
	if builds.Totals.CostUSD != 0.90 || builds.Totals.Tokens != 900 {
		t.Errorf("phase=build totals = %+v, want tokens 900 cost 0.90 (opus+sonnet+kimi builds)", builds.Totals)
	}
	byKey := seriesByKey(builds.Series)
	if _, ok := byKey["codex"]; ok {
		t.Error("phase=build kept a codex series, but codex only ran verify")
	}

	repoFiltered := getTimeseries(t, ts, "?days=7&repo=beta")
	if repoFiltered.Totals.CostUSD != 0.35 || repoFiltered.Totals.Tokens != 350 {
		t.Errorf("repo=beta totals = %+v, want tokens 350 cost 0.35", repoFiltered.Totals)
	}
}

// TestTimeseriesUnknownBucketFilter confirms the "unknown" chip the UI renders for
// empty-provider/empty-phase spend actually filters to that spend: the facet, the
// series key, and the filter must all normalize the empty value the same way, so
// clicking the chip narrows to the unknown bucket rather than an empty result.
func TestTimeseriesUnknownBucketFilter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	seedDayTokens(t, dirs["acme"], "COD-1", time.Now(), []phaseCall{
		{"build", tokens.Record{Input: 300, Output: 200, CostUSD: usd(0.50), Model: "claude-opus-4-8"}},
		{"", tokens.Record{Input: 60, Output: 40, CostUSD: usd(0.02), Model: "mystery-model"}},
	})
	ts := instancesServer(t, home)

	all := getTimeseries(t, ts, "?days=7")
	if !contains(all.Facets.Providers, "unknown") || !contains(all.Facets.Phases, "unknown") {
		t.Fatalf("facets = %+v, want an unknown provider and phase chip", all.Facets)
	}

	byProvider := getTimeseries(t, ts, "?days=7&provider=unknown")
	if len(byProvider.Series) != 1 || byProvider.Series[0].Key != "unknown" {
		t.Fatalf("provider=unknown series = %+v, want a single unknown series", byProvider.Series)
	}
	if byProvider.Totals.CostUSD != 0.02 || byProvider.Totals.Tokens != 100 {
		t.Errorf("provider=unknown totals = %+v, want tokens 100 cost 0.02", byProvider.Totals)
	}

	byPhase := getTimeseries(t, ts, "?days=7&phase=unknown")
	if byPhase.Totals.CostUSD != 0.02 || byPhase.Totals.Tokens != 100 {
		t.Errorf("phase=unknown totals = %+v, want tokens 100 cost 0.02", byPhase.Totals)
	}
}

func contains(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

// TestTimeseriesGroupByDimensions confirms the group-by switch re-keys the series
// along repo, model, and phase.
func TestTimeseriesGroupByDimensions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	seedAnalyticsFixture(t, home)
	ts := instancesServer(t, home)

	byRepo := getTimeseries(t, ts, "?days=7&group_by=repo")
	m := seriesByKey(byRepo.Series)
	if g := m["acme"]; g.CostUSD != 0.80 || g.Tokens != 800 {
		t.Errorf("acme series = %+v, want tokens 800 cost 0.80", g)
	}
	if g := m["beta"]; g.CostUSD != 0.35 || g.Tokens != 350 {
		t.Errorf("beta series = %+v, want tokens 350 cost 0.35", g)
	}

	byPhase := getTimeseries(t, ts, "?days=7&group_by=phase")
	if byPhase.Series[0].Key != "build" || byPhase.Series[0].CostUSD != 0.90 {
		t.Errorf("phase group leads with %+v, want build at 0.90", byPhase.Series[0])
	}

	byModel := getTimeseries(t, ts, "?days=7&group_by=model")
	mm := seriesByKey(byModel.Series)
	if g := mm["claude-opus-4-8"]; g.CostUSD != 0.50 {
		t.Errorf("opus model series = %+v, want cost 0.50", g)
	}
	if _, ok := mm["kimi-k2"]; !ok {
		t.Error("model group missing kimi-k2 series")
	}
}

// TestTimeseriesCompareWindows covers the before/after-a-routing-change contract:
// two adjacent windows requested by explicit from/to return each period's own
// per-provider totals, so the UI can diff them. Here the recent window is Claude
// and the prior window is Codex — the shift a routing change would produce.
func TestTimeseriesCompareWindows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	now := time.Now()
	day := func(n int) time.Time { return now.AddDate(0, 0, n) }
	fmtDay := func(n int) string { return day(n).Format(dateLayout) }

	seedDayTokens(t, dirs["acme"], "COD-NEW", day(-1), []phaseCall{
		{"build", tokens.Record{Input: 600, Output: 400, CostUSD: usd(1.00), Model: "claude-opus-4-8"}},
	})
	seedDayTokens(t, dirs["acme"], "COD-OLD", day(-8), []phaseCall{
		{"build", tokens.Record{Input: 500, Output: 300, CostUSD: usd(0.80), Model: "gpt-5.4"}},
		{"verify", tokens.Record{Input: 60, Output: 40, CostUSD: usd(0.10), Model: "claude-sonnet-5"}},
	})

	ts := instancesServer(t, home)

	recent := getTimeseries(t, ts, "?from="+fmtDay(-6)+"&to="+fmtDay(0))
	if recent.Days != 7 {
		t.Errorf("recent window = %d days, want 7", recent.Days)
	}
	rk := seriesByKey(recent.Series)
	if g := rk["claude"]; g.CostUSD != 1.00 {
		t.Errorf("recent claude = %+v, want cost 1.00", g)
	}
	if _, ok := rk["codex"]; ok {
		t.Error("recent window shows codex, but the routing change moved off it")
	}

	prior := getTimeseries(t, ts, "?from="+fmtDay(-13)+"&to="+fmtDay(-7))
	if prior.Days != 7 {
		t.Errorf("prior window = %d days, want 7", prior.Days)
	}
	pk := seriesByKey(prior.Series)
	if g := pk["codex"]; g.CostUSD != 0.80 {
		t.Errorf("prior codex = %+v, want cost 0.80", g)
	}
	if g := pk["claude"]; g.CostUSD != 0.10 {
		t.Errorf("prior claude = %+v, want cost 0.10", g)
	}
	if recent.Totals.CostUSD != 1.00 || prior.Totals.CostUSD != 0.90 {
		t.Errorf("window totals = recent %v prior %v, want 1.00 / 0.90", recent.Totals.CostUSD, prior.Totals.CostUSD)
	}
}

// TestTimeseriesInlineProviderWins confirms a line's recorded provider takes
// precedence over model derivation, so a Kimi alias that no heuristic would map
// still attributes correctly.
func TestTimeseriesInlineProviderWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	dirs := seedRepos(t, home, "acme")
	seedDayTokens(t, dirs["acme"], "COD-1", time.Now(), []phaseCall{
		{"build", tokens.Record{Input: 100, Output: 100, CostUSD: usd(0.20), Provider: "kimi", Model: "turbo-preview"}},
	})

	ts := instancesServer(t, home)
	r := getTimeseries(t, ts, "?days=7")
	if len(r.Series) != 1 || r.Series[0].Key != "kimi" {
		t.Fatalf("series = %+v, want a single kimi series from the inline provider", r.Series)
	}
}

// TestTimeseriesRejectsNonGET keeps the resource read-only.
func TestTimeseriesRejectsNonGET(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Post(ts.URL+APIPrefix+"/costs/timeseries", "application/json", nil)
	if err != nil {
		t.Fatalf("POST timeseries: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
