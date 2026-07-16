package webserver

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubatlas"
	"github.com/RomkaLTU/trau/internal/registry"
)

// scriptedAgent is a fake agent.Runner that returns a canned Result per call and
// records the prompt it was handed, so a test can assert the retry feedback.
type scriptedAgent struct {
	mu      sync.Mutex
	calls   int
	steps   []agentStep
	prompts []string
	onEnter func(call int)
}

type agentStep struct {
	res agent.Result
	err error
}

func (a *scriptedAgent) Run(_ context.Context, prompt, _ string) (agent.Result, error) {
	a.mu.Lock()
	n := a.calls
	a.calls++
	a.prompts = append(a.prompts, prompt)
	enter := a.onEnter
	a.mu.Unlock()
	if enter != nil {
		enter(n)
	}
	if n < len(a.steps) {
		return a.steps[n].res, a.steps[n].err
	}
	return agent.Result{}, nil
}

func (a *scriptedAgent) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func atlasResult(final string, cost float64) agent.Result {
	return agent.Result{
		Final:   final,
		CostUSD: cost,
		Model:   "claude-opus-4-8",
		Usage:   agent.Usage{Input: 1000, Output: 500},
	}
}

// atlasServer builds a hub over a throwaway home with one registered repo "acme" and
// deterministic commit/clock, ready for a faked generation agent to be installed.
func atlasServer(t *testing.T) (*Server, registry.Repo) {
	t.Helper()
	home := t.TempDir()
	root, runsDir := checkpointRepo(t, home, "acme")
	t.Setenv("HOME", t.TempDir())
	s := New("test", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.atlas.head = func(context.Context, string) string { return "commit-abc" }
	s.atlas.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	return s, registry.Repo{Name: "acme", Root: root, RunsDir: runsDir}
}

func fixedRunner(a agent.Runner) func(config.Config, registry.Repo, hubatlas.View) (agent.Runner, error) {
	return func(config.Config, registry.Repo, hubatlas.View) (agent.Runner, error) {
		return a, nil
	}
}

func TestAtlasGenerateHappyPath(t *testing.T) {
	s, repo := atlasServer(t)
	view, _ := hubatlas.ViewByID("data-model")
	doc := `{"entities":[{"id":"user","name":"User","domain":"core","fields":[{"name":"id","type":"int","pk":true}]}],"relationships":[]}`
	ag := &scriptedAgent{steps: []agentStep{{res: atlasResult(doc, 0.12)}}}
	s.atlas.newRunner = fixedRunner(ag)

	s.atlas.generate(context.Background(), repo, view)

	if ag.callCount() != 1 {
		t.Fatalf("agent calls = %d, want 1", ag.callCount())
	}
	got, ok, err := s.stores.Atlas().Latest(repo.Root, view.ID)
	if err != nil || !ok {
		t.Fatalf("latest doc: ok=%v err=%v", ok, err)
	}
	if got.Commit != "commit-abc" {
		t.Errorf("commit = %q, want commit-abc", got.Commit)
	}
	if !approxEqual(got.CostUSD, 0.12) {
		t.Errorf("cost = %v, want 0.12", got.CostUSD)
	}
	if got.Error != "" {
		t.Errorf("unexpected error row: %q", got.Error)
	}
	if !strings.Contains(got.Document, `"user"`) {
		t.Errorf("document = %q, want the user entity", got.Document)
	}

	spend, err := s.stores.Tokens().Total(repo.Root, atlasBucket)
	if err != nil {
		t.Fatalf("token total: %v", err)
	}
	if spend.Tokens != 1500 {
		t.Errorf("atlas token spend = %d, want 1500", spend.Tokens)
	}
	phases, err := s.stores.Tokens().PhaseTotals(repo.Root, atlasBucket)
	if err != nil {
		t.Fatalf("phase totals: %v", err)
	}
	if len(phases) != 1 || phases[0].Phase != atlasPhase {
		t.Errorf("phase totals = %+v, want one %q category", phases, atlasPhase)
	}
}

func TestAtlasGenerateRetryThenSuccess(t *testing.T) {
	s, repo := atlasServer(t)
	view, _ := hubatlas.ViewByID("data-model")
	good := `{"entities":[{"id":"order","name":"Order","domain":"core","fields":[]}],"relationships":[]}`
	ag := &scriptedAgent{steps: []agentStep{
		{res: atlasResult("not json", 0.5)},
		{res: atlasResult(good, 0.25)},
	}}
	s.atlas.newRunner = fixedRunner(ag)

	s.atlas.generate(context.Background(), repo, view)

	if ag.callCount() != 2 {
		t.Fatalf("agent calls = %d, want 2 (one retry)", ag.callCount())
	}
	if len(ag.prompts) != 2 || !strings.Contains(ag.prompts[1], "rejected") || !strings.Contains(ag.prompts[1], "not json") {
		t.Errorf("retry prompt did not feed back the failure: %q", ag.prompts[1])
	}
	got, ok, err := s.stores.Atlas().Latest(repo.Root, view.ID)
	if err != nil || !ok || !strings.Contains(got.Document, "order") {
		t.Fatalf("latest = %+v ok=%v, want the good order doc", got, ok)
	}
	if got.Error != "" {
		t.Errorf("good document carried an error: %q", got.Error)
	}
	if !approxEqual(got.CostUSD, 0.75) {
		t.Errorf("cost = %v, want 0.75 (both attempts summed)", got.CostUSD)
	}
}

func TestAtlasGenerateRetryThenError(t *testing.T) {
	s, repo := atlasServer(t)
	view, _ := hubatlas.ViewByID("data-model")
	if _, err := s.stores.Atlas().Insert(repo.Root, view.ID, "old-commit", `{"entities":[{"id":"user","name":"User"}]}`, 0.05, ""); err != nil {
		t.Fatalf("seed prior good doc: %v", err)
	}
	ag := &scriptedAgent{steps: []agentStep{
		{res: atlasResult("garbage one", 0.5)},
		{res: atlasResult("garbage two", 0.25)},
	}}
	s.atlas.newRunner = fixedRunner(ag)

	s.atlas.generate(context.Background(), repo, view)

	if ag.callCount() != 2 {
		t.Fatalf("agent calls = %d, want 2", ag.callCount())
	}
	got, ok, err := s.stores.Atlas().Latest(repo.Root, view.ID)
	if err != nil || !ok || got.Commit != "old-commit" {
		t.Fatalf("latest = %+v ok=%v, want the prior good doc undisplaced", got, ok)
	}
	meta, err := s.stores.Atlas().Meta(repo.Root, view.ID)
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if !meta.HasDocument || meta.Commit != "old-commit" {
		t.Errorf("meta lost the good document: %+v", meta)
	}
	if meta.Error == "" {
		t.Errorf("meta did not surface the failure reason after two failures")
	}
}

func TestAtlasGenerateConcurrencyGuard(t *testing.T) {
	s, repo := atlasServer(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	blocking := &scriptedAgent{
		steps: []agentStep{{res: atlasResult(`{"entities":[{"id":"user","name":"User"}]}`, 0.1)}},
		onEnter: func(n int) {
			if n == 0 {
				close(entered)
				<-release
			}
		},
	}
	appFlowsDoc := `{"flows":[{"id":"login","name":"Login","summary":"User signs in.","steps":[{"id":"form","name":"Form","kind":"ui"}],"edges":[]}]}`
	s.atlas.newRunner = func(_ config.Config, _ registry.Repo, v hubatlas.View) (agent.Runner, error) {
		if v.ID == "data-model" {
			return blocking, nil
		}
		return &scriptedAgent{steps: []agentStep{{res: atlasResult(appFlowsDoc, 0.1)}}}, nil
	}

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	gen := func(viewID string) *http.Response {
		return doReq(t, http.MethodPost, ts.URL+APIPrefix+"/repos/acme/atlas/"+viewID+"/generate", nil)
	}

	res1 := gen("data-model")
	if res1.StatusCode != http.StatusAccepted {
		t.Fatalf("first generate = %d, want 202", res1.StatusCode)
	}
	_ = res1.Body.Close()
	<-entered

	res2 := gen("data-model")
	if res2.StatusCode != http.StatusConflict {
		t.Fatalf("second generate for same view = %d, want 409", res2.StatusCode)
	}
	_ = res2.Body.Close()

	res3 := gen("app-flows")
	if res3.StatusCode != http.StatusAccepted {
		t.Fatalf("generate for a different view = %d, want 202", res3.StatusCode)
	}
	_ = res3.Body.Close()

	close(release)
	waitAtlasIdle(t, s)

	if _, ok, _ := s.stores.Atlas().Latest(repo.Root, "data-model"); !ok {
		t.Errorf("data-model document was not stored after the generation finished")
	}
}

func waitAtlasIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.atlas.mu.Lock()
		n := len(s.atlas.inflight)
		s.atlas.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("atlas generations did not settle")
}

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
