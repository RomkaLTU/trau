package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// seedRepo records one exited repo in the known set and returns its runs dir,
// so the runs surface is exercised with no live loop — the "browsable after the
// loop exited" case.
func seedRepo(t *testing.T, home, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	runsDir := filepath.Join(root, ".trau", "runs")
	repo := registry.Repo{Name: name, Root: root, RunsDir: runsDir}
	if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("seed known repo: %v", err)
	}
	return runsDir
}

// seedRepos records several exited repos in the known set and returns each
// repo's runs dir by name, for exercising the machine-wide multiplex.
func seedRepos(t *testing.T, home string, names ...string) map[string]string {
	t.Helper()
	repos := make([]registry.Repo, 0, len(names))
	dirs := map[string]string{}
	for _, name := range names {
		root := filepath.Join(t.TempDir(), name)
		runsDir := filepath.Join(root, ".trau", "runs")
		repos = append(repos, registry.Repo{Name: name, Root: root, RunsDir: runsDir})
		dirs[name] = runsDir
	}
	if err := testStoresAt(t, home).Registrations().Remember(repos); err != nil {
		t.Fatalf("seed known repos: %v", err)
	}
	return dirs
}

// seedCheckpoint writes one ticket's durable state file, key by key, exactly as
// the pipeline does — so the fixtures are the real on-disk shape, not a mock.
func seedCheckpoint(t *testing.T, runsDir, id string, kv map[string]string) {
	t.Helper()
	store := state.NewStore(runsDir)
	for k, v := range kv {
		if err := store.Set(id, k, v); err != nil {
			t.Fatalf("seed %s %s: %v", id, k, err)
		}
	}
}

func getRuns(t *testing.T, ts *httptest.Server, repo string) RunsResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/runs")
	if err != nil {
		t.Fatalf("GET runs: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out RunsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	return out
}

func runByTicket(runs []RunView) map[string]RunView {
	byID := make(map[string]RunView, len(runs))
	for _, r := range runs {
		byID[r.Ticket] = r
	}
	return byID
}

// TestRunsBoardCoversEveryPhaseAndFailureClass is the fixture-driven contract
// test: a repo the hub no longer has a live loop in still exposes one run per
// checkpoint phase, with the three failure classes flagged and their reasons
// carried through.
func TestRunsBoardCoversEveryPhaseAndFailureClass(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")

	seedCheckpoint(t, runsDir, "COD-1", map[string]string{"PHASE": state.Building, "TITLE": "wire up the thing"})
	seedCheckpoint(t, runsDir, "COD-2", map[string]string{"PHASE": state.Built})
	seedCheckpoint(t, runsDir, "COD-3", map[string]string{"PHASE": state.HandedOff})
	seedCheckpoint(t, runsDir, "COD-4", map[string]string{"PHASE": state.Verified})
	seedCheckpoint(t, runsDir, "COD-5", map[string]string{
		"PHASE": state.PROpen, "BRANCH": "feature/COD-5-x", "PR": "42",
		"PR_URL": "https://github.com/acme/acme/pull/42",
	})
	seedCheckpoint(t, runsDir, "COD-6", map[string]string{"PHASE": state.Merged, "PR": "7"})
	seedCheckpoint(t, runsDir, "COD-7", map[string]string{
		"PHASE": state.Quarantined, "FAILURE_REASON": "verify failed after repairs",
	})
	seedCheckpoint(t, runsDir, "COD-8", map[string]string{
		"PHASE": state.Built, "FAILURE_CLASS": state.FailPaused,
		"FAILURE_REASON": "claude rate/usage limit reached",
	})
	seedCheckpoint(t, runsDir, "COD-9", map[string]string{
		"PHASE": state.HandedOff, "FAILURE_CLASS": state.FailFaulted,
		"FAILURE_REASON": "unexpected error during verify: boom",
	})

	ts := instancesServer(t, home)
	out := getRuns(t, ts, "acme")

	if out.Repo != "acme" {
		t.Errorf("Repo = %q, want acme", out.Repo)
	}
	if len(out.Runs) != 9 {
		t.Fatalf("runs = %d, want 9", len(out.Runs))
	}

	prevRank := -1
	for _, r := range out.Runs {
		if r.PhaseRank < prevRank {
			t.Errorf("board not ordered by phase rank: %s rank %d after %d", r.Ticket, r.PhaseRank, prevRank)
		}
		prevRank = r.PhaseRank
	}

	byID := runByTicket(out.Runs)
	cases := []struct {
		id            string
		phase         string
		rank          int
		terminal      bool
		failureClass  string
		failureReason string
	}{
		{"COD-1", state.Building, 1, false, "", ""},
		{"COD-2", state.Built, 2, false, "", ""},
		{"COD-3", state.HandedOff, 3, false, "", ""},
		{"COD-4", state.Verified, 4, false, "", ""},
		{"COD-5", state.PROpen, 5, false, "", ""},
		{"COD-6", state.Merged, 6, true, "", ""},
		{"COD-7", state.Quarantined, 9, true, state.FailGaveUp, "verify failed after repairs"},
		{"COD-8", state.Built, 2, false, state.FailPaused, "claude rate/usage limit reached"},
		{"COD-9", state.HandedOff, 3, false, state.FailFaulted, "unexpected error during verify: boom"},
	}
	for _, c := range cases {
		r, ok := byID[c.id]
		if !ok {
			t.Errorf("%s missing from board", c.id)
			continue
		}
		if r.Phase != c.phase || r.PhaseRank != c.rank {
			t.Errorf("%s phase = %q/%d, want %q/%d", c.id, r.Phase, r.PhaseRank, c.phase, c.rank)
		}
		if r.Terminal != c.terminal {
			t.Errorf("%s terminal = %v, want %v", c.id, r.Terminal, c.terminal)
		}
		if r.FailureClass != c.failureClass {
			t.Errorf("%s failure_class = %q, want %q", c.id, r.FailureClass, c.failureClass)
		}
		if r.FailureReason != c.failureReason {
			t.Errorf("%s failure_reason = %q, want %q", c.id, r.FailureReason, c.failureReason)
		}
	}

	if r := byID["COD-1"]; r.Title != "wire up the thing" {
		t.Errorf("COD-1 title = %q, want it carried through", r.Title)
	}
	if r := byID["COD-5"]; r.Branch != "feature/COD-5-x" || r.PR != "42" || r.PRURL == "" {
		t.Errorf("COD-5 branch/pr = %+v, want the PR reference carried through", r)
	}
}

// TestRunsMergedDropsStaleFailure guards the precedence rule: a run that paused,
// then resumed and merged, must read as a clean terminal merge even if a stale
// marker lingers on its checkpoint.
func TestRunsMergedDropsStaleFailure(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-1", map[string]string{
		"PHASE": state.Merged, "FAILURE_CLASS": state.FailPaused,
		"FAILURE_REASON": "claude rate/usage limit reached",
	})

	ts := instancesServer(t, home)
	r := runByTicket(getRuns(t, ts, "acme").Runs)["COD-1"]

	if !r.Terminal || r.FailureClass != "" || r.FailureReason != "" {
		t.Errorf("merged run = %+v, want terminal with no failure flag", r)
	}
}

// TestReposListsKnownRepos covers the /repos resource: an exited repo is listed
// and flagged not live.
func TestReposListsKnownRepos(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")

	ts := instancesServer(t, home)
	res, err := http.Get(ts.URL + APIPrefix + "/repos")
	if err != nil {
		t.Fatalf("GET repos: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out ReposResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	if len(out.Repos) != 1 {
		t.Fatalf("repos = %d, want 1", len(out.Repos))
	}
	if out.Repos[0].Name != "acme" || out.Repos[0].Live {
		t.Errorf("repo = %+v, want acme not live", out.Repos[0])
	}
}

// TestRunsUnknownRepo404 covers the miss: a repo the hub never saw is a JSON 404,
// not the SPA shell.
func TestRunsUnknownRepo404(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/runs")
	if err != nil {
		t.Fatalf("GET runs: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// TestRunsRejectsNonGET keeps the resource read-only.
func TestRunsRejectsNonGET(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	res, err := http.Post(ts.URL+APIPrefix+"/repos/acme/runs", "application/json", nil)
	if err != nil {
		t.Fatalf("POST runs: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}
