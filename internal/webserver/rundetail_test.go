package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// seedTokenCalls appends the given per-phase calls to the authoritative token store
// under the repo that owns runsDir, tagged with ts — the DB-first shape the loop now
// posts (ADR 0008), replacing the old per-run tokens.jsonl.
func seedTokenCalls(t *testing.T, home, runsDir, id, ts string, calls []phaseCall) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(runsDir))
	rows := make([]hubstore.TokenCall, 0, len(calls))
	for _, c := range calls {
		total := c.rec.Input + c.rec.Output + c.rec.CacheRead + c.rec.CacheCreation
		if total <= 0 {
			continue
		}
		rows = append(rows, hubstore.TokenCall{
			Ticket:        id,
			TS:            ts,
			Phase:         c.phase,
			Input:         c.rec.Input,
			Output:        c.rec.Output,
			CacheRead:     c.rec.CacheRead,
			CacheCreation: c.rec.CacheCreation,
			Reasoning:     c.rec.Reasoning,
			Total:         total,
			CostUSD:       c.rec.CostUSD,
			Turns:         c.rec.Turns,
			IsError:       c.rec.IsError,
			Provider:      c.rec.Provider,
			Model:         c.rec.Model,
			Context:       c.rec.Context,
		})
	}
	if err := testStoresAt(t, home).Tokens().Append(root, rows); err != nil {
		t.Fatalf("seed token calls: %v", err)
	}
}

// seedTokens appends the given per-phase calls at a fixed timestamp — for detail
// tests where only the per-phase breakdown matters, not the date.
func seedTokens(t *testing.T, home, runsDir, id string, calls []phaseCall) {
	t.Helper()
	seedTokenCalls(t, home, runsDir, id, "2026-07-12T12:00:00", calls)
}

// seedAnomalies detects and records the anomalies for the given per-phase spend on
// the ticket, exercising DetectAnomalies plus the store round-trip.
func seedAnomalies(t *testing.T, home, runsDir, id string, phases []tokens.PhaseSpend) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(runsDir))
	detected := tokens.DetectAnomalies(phases)
	rows := make([]hubstore.Anomaly, len(detected))
	for i, a := range detected {
		rows[i] = hubstore.Anomaly{
			TS:      "2026-07-12T12:00:00",
			Phase:   a.Phase,
			Output:  a.Output,
			Turns:   a.Turns,
			Cost:    a.Cost,
			Reasons: a.Reasons,
		}
	}
	if err := testStoresAt(t, home).Tokens().RecordAnomalies(root, id, rows); err != nil {
		t.Fatalf("seed anomalies: %v", err)
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
	seedTokens(t, home, runsDir, "COD-100", []phaseCall{
		{"build", tokens.Record{Input: 100, Output: 50, CacheRead: 10, CacheCreation: 5, CostUSD: usd(0.10), Turns: 3}},
		{"build", tokens.Record{Input: 200, Output: 80, CacheRead: 20, CostUSD: usd(0.20), Turns: 4}},
		{"handoff", tokens.Record{Input: 300, Output: 120, CostUSD: usd(0.05), Turns: 2}},
		{"verify", tokens.Record{Input: 400, Output: 200, CacheRead: 50, CacheCreation: 10, CostUSD: usd(0.30), Turns: 5}},
		{"commit", tokens.Record{Input: 50, Output: 20, CostUSD: usd(0.01), Turns: 1}},
	})
	seedArtifact(t, runsDir, "COD-100", "handoff.md", "# QA brief\n\n- Check the detail renders.\n")
	seedArtifact(t, runsDir, "COD-100", "rubric.json", `{"ticket":"COD-100","acceptance_criteria":["detail returns state keys","cost table sums exactly"],"non_goals":["editing runs"],"required_tests":["rundetail_test.go"],"ui_paths":["/runs/acme/COD-100"],"fail_conditions":["missing artifact errors"]}`)
	seedArtifact(t, runsDir, "COD-100", "verdict.json", `{"pass":true,"summary":"all criteria hold","failures":[],"checks":[{"name":"tests","severity":"error","pass":true,"detail":"go test ok"}]}`)
	seedArtifact(t, runsDir, "COD-100", "buildnotes.md", "files: internal/webserver/rundetail.go\ntest: rundetail_test.go\n")

	ts := instancesServer(t, home)
	d := getRunDetail(t, ts, "acme", "COD-100")

	if d.Ticket != "COD-100" || d.Title != "wire the run detail page" || d.Phase != state.PROpen {
		t.Errorf("state keys = %+v, want ticket/title/phase carried through", d.RunView)
	}
	if d.Branch != "feature/COD-100-run-detail" || d.PR != "42" || d.PRURL == "" {
		t.Errorf("PR reference = branch %q pr %q url %q, want carried through", d.Branch, d.PR, d.PRURL)
	}

	if !d.Artifacts.Handoff || !d.Artifacts.Rubric || !d.Artifacts.Verdict || !d.Artifacts.BuildNotes || !d.Artifacts.Tokens {
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
	if d.BuildNotes == "" {
		t.Error("build notes missing, want the notes carried through from the store")
	}

	// The first GET folds the legacy files into the store and removes them: a
	// completed run leaves no artifact files behind (AC #1, #4).
	for _, name := range []string{"handoff.md", "rubric.json", "verdict.json", "buildnotes.md"} {
		if _, err := os.Stat(filepath.Join(runsDir, "COD-100", name)); !os.IsNotExist(err) {
			t.Errorf("legacy artifact %s survived the store cutover (err=%v)", name, err)
		}
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

// TestRunDetailReadsArtifactsFromStore covers the post-cutover path: artifacts
// posted straight to the hub (no legacy files on disk) render on the detail page,
// with build notes surfaced from the store.
func TestRunDetailReadsArtifactsFromStore(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	seedCheckpoint(t, runsDir, "COD-300", map[string]string{"PHASE": state.Verified, "TITLE": "store-native run"})

	arts := testStoresAt(t, home).Artifacts()
	_ = arts.Upsert(root, "COD-300", hubstore.ArtifactHandoff, "# brief from the hub")
	_ = arts.Upsert(root, "COD-300", hubstore.ArtifactVerdict, `{"pass":false,"summary":"one failure","failures":["boom"]}`)
	_ = arts.Upsert(root, "COD-300", hubstore.ArtifactBuildNotes, "notes straight to the store")

	ts := instancesServer(t, home)
	d := getRunDetail(t, ts, "acme", "COD-300")

	if d.Handoff != "# brief from the hub" || !d.Artifacts.Handoff {
		t.Errorf("handoff = %q present=%v, want the stored brief", d.Handoff, d.Artifacts.Handoff)
	}
	if d.Verdict == nil || d.Verdict.Pass || len(d.Verdict.Failures) != 1 {
		t.Errorf("verdict = %+v, want a failing verdict with one failure", d.Verdict)
	}
	if d.BuildNotes != "notes straight to the store" || !d.Artifacts.BuildNotes {
		t.Errorf("build notes = %q present=%v, want the stored notes", d.BuildNotes, d.Artifacts.BuildNotes)
	}
	if d.Rubric != nil || d.Artifacts.Rubric {
		t.Errorf("rubric = %+v present=%v, want absent (never stored)", d.Rubric, d.Artifacts.Rubric)
	}
}

// TestRunDetailArtifactPresentButEmpty covers the three-way present/empty/absent
// distinction (ADR 0008): a rubric or verdict row that exists with empty or
// malformed content flags present — so the page renders present-but-empty apart
// from not-yet-produced — while its parsed field degrades to nil, not a 500.
func TestRunDetailArtifactPresentButEmpty(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	seedCheckpoint(t, runsDir, "COD-301", map[string]string{"PHASE": state.Verified})

	arts := testStoresAt(t, home).Artifacts()
	_ = arts.Upsert(root, "COD-301", hubstore.ArtifactRubric, "")
	_ = arts.Upsert(root, "COD-301", hubstore.ArtifactVerdict, "{not valid json")

	d := getRunDetail(t, instancesServer(t, home), "acme", "COD-301")

	if !d.Artifacts.Rubric || d.Rubric != nil {
		t.Errorf("rubric present=%v value=%+v, want present with a nil parse (empty content)", d.Artifacts.Rubric, d.Rubric)
	}
	if !d.Artifacts.Verdict || d.Verdict != nil {
		t.Errorf("verdict present=%v value=%+v, want present with a nil parse (malformed content)", d.Artifacts.Verdict, d.Verdict)
	}
	if d.Artifacts.Handoff || d.Artifacts.BuildNotes {
		t.Errorf("artifacts = %+v, want handoff/buildnotes absent (no row)", d.Artifacts)
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
	seedTokens(t, home, runsDir, "COD-101", []phaseCall{
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

// TestRunDetailSurfacesAnomalies covers the run-detail side of the anomalies
// list: a run whose phase tripped a soft cost threshold carries its flagged
// anomalies, and a quiet run carries none.
func TestRunDetailSurfacesAnomalies(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-9", map[string]string{"PHASE": state.Building})

	seedAnomalies(t, home, runsDir, "COD-9", []tokens.PhaseSpend{
		{Phase: "cleanup", Output: 120_000, Turns: 8, Cost: 6.50},
	})

	ts := instancesServer(t, home)
	d := getRunDetail(t, ts, "acme", "COD-9")

	if len(d.Anomalies) != 1 {
		t.Fatalf("anomalies = %d, want the single cleanup trip", len(d.Anomalies))
	}
	if a := d.Anomalies[0]; a.Phase != "cleanup" || a.CostUSD != 6.5 || len(a.Reasons) == 0 {
		t.Errorf("anomaly = %+v, want cleanup at $6.50 with reasons", a)
	}

	root := filepath.Dir(filepath.Dir(runsDir))
	if err := testStoresAt(t, home).Checkpoints().Upsert(root, "COD-10", map[string]string{"PHASE": state.Building}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	seedTokens(t, home, runsDir, "COD-10", []phaseCall{
		{"build", tokens.Record{Output: 100, Turns: 2, CostUSD: usd(0.10)}},
	})
	if quiet := getRunDetail(t, ts, "acme", "COD-10"); len(quiet.Anomalies) != 0 {
		t.Errorf("quiet run anomalies = %+v, want none", quiet.Anomalies)
	}
}

// TestRunDetailSurfacesNoSkillsWarning covers the run-detail side of the
// skill-less build warning: a run with a build_no_skills event in the table
// carries no_skills, and a run without one does not — even when another ticket in
// the same repo was flagged.
func TestRunDetailSurfacesNoSkillsWarning(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-200", map[string]string{"PHASE": state.Built})
	seedCheckpoint(t, runsDir, "COD-201", map[string]string{"PHASE": state.Built})

	ts := instancesServer(t, home)
	postEvents(t, ts, "acme",
		hubclient.Event{Kind: event.KindAgentStart, Phase: "build", Fields: `{"ticket":"COD-200"}`},
		hubclient.Event{Kind: event.KindBuildNoSkills, Phase: "build", Fields: `{"ticket":"COD-200"}`},
	)

	if d := getRunDetail(t, ts, "acme", "COD-200"); !d.NoSkills {
		t.Error("no_skills = false, want the flagged build surfaced")
	}
	if d := getRunDetail(t, ts, "acme", "COD-201"); d.NoSkills {
		t.Error("no_skills = true for an unflagged run, want false")
	}
}

// TestRunDetailServesHubOnlyCheckpoint covers the post-cutover run: a ticket that
// exists only as an authoritative checkpoint row, with no legacy state file on
// disk, still resolves to a 200 detail carrying its phase, branch, and PR — the
// board and the detail page read the same table.
func TestRunDetailServesHubOnlyCheckpoint(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))

	if err := testStoresAt(t, home).Checkpoints().Upsert(root, "COD-7001", map[string]string{
		"PHASE": state.PROpen, "TITLE": "hub-only run", "BRANCH": "feature/COD-7001",
		"PR": "7", "PR_URL": "https://github.com/acme/acme/pull/7",
	}); err != nil {
		t.Fatalf("seed checkpoint row: %v", err)
	}
	if stateFileExists(runsDir, "COD-7001") {
		t.Fatal("state file present, want a hub-only checkpoint with no file on disk")
	}

	ts := instancesServer(t, home)
	d := getRunDetail(t, ts, "acme", "COD-7001")

	if d.Ticket != "COD-7001" || d.Phase != state.PROpen || d.Title != "hub-only run" {
		t.Errorf("detail = %+v, want the checkpoint's ticket/phase/title carried through", d.RunView)
	}
	if d.Branch != "feature/COD-7001" || d.PR != "7" || d.PRURL == "" {
		t.Errorf("PR reference = branch %q pr %q url %q, want carried through", d.Branch, d.PR, d.PRURL)
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
