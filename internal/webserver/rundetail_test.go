package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// seedTokens appends the given per-phase calls to runs/<id>/tokens.jsonl through
// the real Sink, so the detail resource reads the exact on-disk log shape the
// pipeline writes.
func seedTokens(t *testing.T, runsDir, id string, calls []phaseCall) {
	t.Helper()
	sink := tokens.New(runsDir)
	sink.SetTicket(id)
	for _, c := range calls {
		sink.Append(c.phase, c.rec)
	}
}

type phaseCall struct {
	phase string
	rec   tokens.Record
}

func seedArtifact(t *testing.T, runsDir, id, name, content string) {
	t.Helper()
	dir := filepath.Join(runsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func getRunDetail(t *testing.T, ts *httptest.Server, repo, ticket string) RunDetail {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/runs/" + ticket)
	if err != nil {
		t.Fatalf("GET run detail: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out RunDetail
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode run detail: %v", err)
	}
	return out
}

func usd(v float64) *float64 { return &v }

func costByPhase(costs []PhaseCost) map[string]PhaseCost {
	byPhase := make(map[string]PhaseCost, len(costs))
	for _, c := range costs {
		byPhase[c.Phase] = c
	}
	return byPhase
}

// TestRunDetailCompleteRun is the fixture-driven contract test for a run that has
// been through the whole pipeline: it exposes the checkpoint state keys, the
// per-phase cost table summed exactly from tokens.jsonl, and all three QA
// artifacts (handoff, rubric, verdict) with a working PR reference.
func TestRunDetailCompleteRun(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")

	seedCheckpoint(t, runsDir, "COD-100", map[string]string{
		"PHASE": state.PROpen, "TITLE": "wire the run detail page",
		"BRANCH": "feature/COD-100-run-detail", "PR": "42",
		"PR_URL": "https://github.com/acme/acme/pull/42",
	})
	seedTokens(t, runsDir, "COD-100", []phaseCall{
		{"build", tokens.Record{Input: 100, Output: 50, CacheRead: 10, CacheCreation: 5, CostUSD: usd(0.10), Turns: 3}},
		{"build", tokens.Record{Input: 200, Output: 80, CacheRead: 20, CostUSD: usd(0.20), Turns: 4}},
		{"handoff", tokens.Record{Input: 300, Output: 120, CostUSD: usd(0.05), Turns: 2}},
		{"verify", tokens.Record{Input: 400, Output: 200, CacheRead: 50, CacheCreation: 10, CostUSD: usd(0.30), Turns: 5}},
		{"commit", tokens.Record{Input: 50, Output: 20, CostUSD: usd(0.01), Turns: 1}},
	})
	seedArtifact(t, runsDir, "COD-100", "handoff.md", "# QA brief\n\n- Check the detail renders.\n")
	seedArtifact(t, runsDir, "COD-100", "rubric.json", `{"ticket":"COD-100","acceptance_criteria":["detail returns state keys","cost table sums exactly"],"non_goals":["editing runs"],"required_tests":["rundetail_test.go"],"ui_paths":["/runs/acme/COD-100"],"fail_conditions":["missing artifact errors"]}`)
	seedArtifact(t, runsDir, "COD-100", "verdict.json", `{"pass":true,"summary":"all criteria hold","failures":[],"checks":[{"name":"tests","severity":"error","pass":true,"detail":"go test ok"}]}`)

	ts := instancesServer(t, home)
	d := getRunDetail(t, ts, "acme", "COD-100")

	if d.Ticket != "COD-100" || d.Title != "wire the run detail page" || d.Phase != state.PROpen {
		t.Errorf("state keys = %+v, want ticket/title/phase carried through", d.RunView)
	}
	if d.Branch != "feature/COD-100-run-detail" || d.PR != "42" || d.PRURL == "" {
		t.Errorf("PR reference = branch %q pr %q url %q, want carried through", d.Branch, d.PR, d.PRURL)
	}

	if !d.Artifacts.Handoff || !d.Artifacts.Rubric || !d.Artifacts.Verdict || !d.Artifacts.Tokens {
		t.Errorf("artifacts = %+v, want all present", d.Artifacts)
	}
	if d.Handoff == "" {
		t.Error("handoff brief missing, want the markdown carried through")
	}
	if d.Rubric == nil || len(d.Rubric.AcceptanceCriteria) != 2 {
		t.Errorf("rubric = %+v, want two acceptance criteria", d.Rubric)
	}
	if d.Verdict == nil || !d.Verdict.Pass || len(d.Verdict.Checks) != 1 {
		t.Errorf("verdict = %+v, want a passing verdict with one check", d.Verdict)
	}

	if len(d.Costs) != 4 {
		t.Fatalf("cost rows = %d, want 4 phases", len(d.Costs))
	}
	wantOrder := []string{"build", "handoff", "verify", "commit"}
	for i, phase := range wantOrder {
		if d.Costs[i].Phase != phase {
			t.Errorf("cost row %d = %q, want %q (first-seen order)", i, d.Costs[i].Phase, phase)
		}
	}

	byPhase := costByPhase(d.Costs)
	build := byPhase["build"]
	want := PhaseCost{
		Phase: "build", Input: 300, Output: 130, CacheRead: 30, CacheCreation: 5,
		Total: 465, CostUSD: 0.30, Metered: true, Calls: 2, Turns: 7,
	}
	if build != want {
		t.Errorf("build phase = %+v, want exact sums %+v", build, want)
	}
	if c := byPhase["handoff"]; c.Total != 420 || c.CostUSD != 0.05 || c.Calls != 1 {
		t.Errorf("handoff phase = %+v, want total 420 cost 0.05", c)
	}
	if c := byPhase["verify"]; c.Total != 660 || c.CostUSD != 0.30 {
		t.Errorf("verify phase = %+v, want total 660 cost 0.30", c)
	}
	if c := byPhase["commit"]; c.Total != 70 || c.CostUSD != 0.01 {
		t.Errorf("commit phase = %+v, want total 70 cost 0.01", c)
	}
}

// TestRunDetailPartialEarlyPhaseRun covers the degrade-gracefully contract: a run
// still in its first phase has token spend but no handoff, rubric, verdict, or PR.
// The resource returns 200 with those artifacts absent rather than erroring, and a
// codex-subscription call with no per-call cost marks the phase unmetered.
func TestRunDetailPartialEarlyPhaseRun(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")

	seedCheckpoint(t, runsDir, "COD-101", map[string]string{
		"PHASE": state.Building, "TITLE": "just started",
	})
	seedTokens(t, runsDir, "COD-101", []phaseCall{
		{"build", tokens.Record{Input: 100, Output: 50, Turns: 3}},
	})

	ts := instancesServer(t, home)
	d := getRunDetail(t, ts, "acme", "COD-101")

	if d.Ticket != "COD-101" || d.Phase != state.Building {
		t.Errorf("state = %+v, want an early-phase building run", d.RunView)
	}
	if d.Branch != "" || d.PR != "" || d.PRURL != "" {
		t.Errorf("PR reference = %+v, want none yet", d.RunView)
	}
	if d.Handoff != "" || d.Rubric != nil || d.Verdict != nil {
		t.Errorf("QA artifacts = handoff %q rubric %v verdict %v, want none yet", d.Handoff, d.Rubric, d.Verdict)
	}
	if d.Artifacts.Handoff || d.Artifacts.Rubric || d.Artifacts.Verdict {
		t.Errorf("artifacts = %+v, want QA artifacts absent", d.Artifacts)
	}
	if !d.Artifacts.Tokens {
		t.Error("tokens artifact = false, want the early build spend recorded")
	}

	if len(d.Costs) != 1 {
		t.Fatalf("cost rows = %d, want just the build phase", len(d.Costs))
	}
	build := d.Costs[0]
	if build.Phase != "build" || build.Total != 150 || build.Calls != 1 {
		t.Errorf("build phase = %+v, want total 150 one call", build)
	}
	if build.Metered || build.CostUSD != 0 {
		t.Errorf("build phase = %+v, want unmetered with no measured cost", build)
	}
}

// TestRunDetailUnknownRun404 covers the miss: a ticket with no checkpoint under a
// known repo is a JSON 404, not the SPA shell.
func TestRunDetailUnknownRun404(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/runs/COD-404")
	if err != nil {
		t.Fatalf("GET run detail: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// TestRunDetailUnknownRepo404 covers a repo the hub never saw.
func TestRunDetailUnknownRepo404(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/runs/COD-1")
	if err != nil {
		t.Fatalf("GET run detail: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// TestRunDetailRejectsNonGET keeps the resource read-only.
func TestRunDetailRejectsNonGET(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Building})
	ts := instancesServer(t, home)

	res, err := http.Post(ts.URL+APIPrefix+"/repos/acme/runs/COD-1", "application/json", nil)
	if err != nil {
		t.Fatalf("POST run detail: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}
