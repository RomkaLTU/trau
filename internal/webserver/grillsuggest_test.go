package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// blockingAgent never answers on its own, standing in for a classifier that outlives
// the hard timeout.
type blockingAgent struct{}

func (blockingAgent) Run(ctx context.Context, _, _ string) (agent.Result, error) {
	<-ctx.Done()
	return agent.Result{}, ctx.Err()
}

// suggestServer builds a hub over a throwaway home with one registered repo "acme"
// and the classification agent replaced by runner.
func suggestServer(t *testing.T, runner agent.Runner) (*httptest.Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	stores := testStoresAt(t, home)
	repo := registry.Repo{Name: "acme", Root: filepath.Join(home, "acme"), RunsDir: filepath.Join(home, "acme", ".trau", "runs")}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, stores)
	s.newHubAgent = func(config.Config, registry.Repo) (agent.Runner, error) { return runner, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, repo.Name
}

func suggestURL(ts *httptest.Server, repo string) string {
	return ts.URL + APIPrefix + "/repos/" + repo + "/grill/suggest-mode"
}

func suggestMode(t *testing.T, ts *httptest.Server, repo, text string) SuggestModeResponse {
	t.Helper()
	res := postJSON(t, suggestURL(ts, repo), SuggestModeRequest{Text: text})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("suggest status = %d, want 200", res.StatusCode)
	}
	var v SuggestModeResponse
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode suggest: %v", err)
	}
	return v
}

func TestSuggestModeClassifiesResearch(t *testing.T) {
	ag := &scriptedAgent{steps: []agentStep{{res: agent.Result{Final: "research\n"}}}}
	ts, repo := suggestServer(t, ag)

	got := suggestMode(t, ts, repo, "compare the oauth libraries vendor Y ships")
	if got.Mode != hubstore.GrillModeResearch || !got.Suggested {
		t.Fatalf("suggest = %+v, want research suggested", got)
	}
	if ag.callCount() != 1 {
		t.Fatalf("classifier calls = %d, want 1", ag.callCount())
	}
	if !strings.Contains(ag.prompts[0], "compare the oauth libraries vendor Y ships") {
		t.Fatalf("prompt does not carry the note: %q", ag.prompts[0])
	}
}

func TestSuggestModeInterviewForClarification(t *testing.T) {
	ts, repo := suggestServer(t, &scriptedAgent{steps: []agentStep{{res: agent.Result{Final: "interview"}}}})

	if got := suggestMode(t, ts, repo, "what should the empty state say?"); got.Mode != hubstore.GrillModeInterview {
		t.Fatalf("mode = %q, want interview", got.Mode)
	}
}

// A classifier that outruns the hard timeout leaves the default standing rather than
// failing the request — the create flow must stay fully usable — and says so, so the
// chip is not labelled as read from the note.
func TestSuggestModeTimesOutToInterview(t *testing.T) {
	restore := grillSuggestTimeout
	grillSuggestTimeout = 20 * time.Millisecond
	t.Cleanup(func() { grillSuggestTimeout = restore })
	ts, repo := suggestServer(t, blockingAgent{})

	got := suggestMode(t, ts, repo, "compare the oauth libraries")
	if got.Mode != hubstore.GrillModeInterview || got.Suggested {
		t.Fatalf("suggest = %+v, want unsuggested interview", got)
	}
}

func TestSuggestModeGarbageOutputFallsBackToInterview(t *testing.T) {
	for _, final := range []string{
		"",
		"I think this one is probably research, but it depends.",
		`{"mode":"research"}`,
		"researching",
	} {
		t.Run(final, func(t *testing.T) {
			ts, repo := suggestServer(t, &scriptedAgent{steps: []agentStep{{res: agent.Result{Final: final}}}})

			got := suggestMode(t, ts, repo, "compare the oauth libraries")
			if got.Mode != hubstore.GrillModeInterview || got.Suggested {
				t.Fatalf("suggest = %+v, want unsuggested interview", got)
			}
		})
	}
}

// An empty note has nothing to classify, so it answers the default without spending a
// call.
func TestSuggestModeEmptyTextSkipsTheAgent(t *testing.T) {
	ag := &scriptedAgent{steps: []agentStep{{res: agent.Result{Final: "research"}}}}
	ts, repo := suggestServer(t, ag)

	if got := suggestMode(t, ts, repo, "   "); got.Mode != hubstore.GrillModeInterview {
		t.Fatalf("mode = %q, want interview", got.Mode)
	}
	if ag.callCount() != 0 {
		t.Fatalf("classifier calls = %d, want 0", ag.callCount())
	}
}

// The classifier is a composer affordance that reads nothing, so it runs on the
// pairing the composer names — not the loop's deep model — undeliberated, and under a
// mechanical phase so the child starts without the repo's MCP servers. Anything slower
// answers after the user has already picked.
func TestSuggestModeClassifierRunsLean(t *testing.T) {
	cfg := grillSuggestConfig(config.Config{
		Provider:     "codex",
		ClaudeModel:  "claude-opus-5",
		ClaudeEffort: "xhigh",
		GrillModel:   "claude-fable-5",
	})

	if cfg.Provider != "claude" || cfg.ClaudeModel != "claude-fable-5" || cfg.ClaudeEffort != "" {
		t.Fatalf("classifier runs %s/%s effort %q, want claude/claude-fable-5 with no effort", cfg.Provider, cfg.ClaudeModel, cfg.ClaudeEffort)
	}
	if !agent.MechanicalPhase(grillSuggestPhase) {
		t.Fatalf("phase %q is not mechanical, so the classifier pays the repo's MCP startup", grillSuggestPhase)
	}
}

func TestSuggestModeRejectsUnknownRepo(t *testing.T) {
	ts, _ := suggestServer(t, &scriptedAgent{})

	res := postJSON(t, suggestURL(ts, "nope"), SuggestModeRequest{Text: "compare things"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("suggest status = %d, want 404", res.StatusCode)
	}
}
