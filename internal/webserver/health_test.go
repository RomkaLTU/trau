package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// healthServer builds a loopback hub with an empty user config and returns the
// server, its test HTTP endpoint, and the store so a test can seed repo config
// and sync state before reading health.
func healthServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// registerRepoAt creates a git repo, writes its .trau.ini when ini is non-empty,
// registers it with the hub, and returns its root.
func registerRepoAt(t *testing.T, s *Server, name, ini string) string {
	t.Helper()
	root := gitRepo(t, t.TempDir(), name, "dir")
	if ini != "" {
		writeRepoINI(t, root, ini)
	}
	if err := s.stores.Registrations().Register(root); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return root
}

func getHealth(t *testing.T, ts *httptest.Server, name string) (int, RepoHealth) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + name + "/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var h RepoHealth
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&h); err != nil {
			t.Fatalf("decode health: %v", err)
		}
	}
	return res.StatusCode, h
}

const (
	linearINI   = "TRACKER_PROVIDER=linear\nLINEAR_API_KEY=k\nLINEAR_TEAM=COD\n"
	jiraINI     = "JIRA_BASE_URL=https://acme.atlassian.net\nJIRA_EMAIL=dev@acme.io\nJIRA_API_TOKEN=tok\n"
	internalINI = "TRACKER_PROVIDER=internal\n"
)

func TestDeriveHealthState(t *testing.T) {
	synced := hubstore.SyncState{LastSyncedAt: "2026-07-14T00:00:00Z", LastIssues: 3}
	failedAfterGood := hubstore.SyncState{
		LastSyncedAt: "2026-07-14T00:00:00Z",
		LastError:    "linear: issue not found",
	}
	tests := []struct {
		name     string
		provider string
		syncing  bool
		st       hubstore.SyncState
		want     RepoHealthState
	}{
		{"a pull in flight wins over the last outcome", "linear", true, failedAfterGood, HealthSyncing},
		{"a recorded error is sync-failed over a synced stamp", "linear", false, failedAfterGood, HealthSyncFailed},
		{"a clean synced stamp is ready", "linear", false, synced, HealthReady},
		{"configured with no sync bookkeeping is never-synced", "linear", false, hubstore.SyncState{}, HealthNeverSynced},
		{"unconfigured with no sync bookkeeping", "", false, hubstore.SyncState{}, HealthUnconfigured},
		{"an internal provider is ready with no bookkeeping", "internal", false, hubstore.SyncState{}, HealthReady},
		{"an internal provider sheds a stale sync error", "internal", false, failedAfterGood, HealthReady},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveHealthState(tc.provider, tc.syncing, tc.st); got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRepoHealthUnconfigured(t *testing.T) {
	s, ts := healthServer(t)
	registerRepoAt(t, s, "fresh", "")

	code, h := getHealth(t, ts, "fresh")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if h.State != HealthUnconfigured {
		t.Fatalf("state = %q, want unconfigured for a registered repo with no tracker config", h.State)
	}
	if h.LastSyncedAt != "" || h.LastError != "" || h.IssueCount != 0 {
		t.Fatalf("health = %+v, want no sync facts and zero issues", h)
	}
}

func TestRepoHealthNeverSynced(t *testing.T) {
	s, ts := healthServer(t)
	registerRepoAt(t, s, "loop", linearINI)

	_, h := getHealth(t, ts, "loop")
	if h.State != HealthNeverSynced {
		t.Fatalf("state = %q, want never-synced for a configured repo with no sync yet", h.State)
	}
}

// TestRepoHealthSyncFailedMelga is the melga case: registered, Jira credentials,
// TRACKER_PROVIDER unset, a recorded linear error over an otherwise configured
// repo. Health must read sync-failed and carry the error, never ready.
func TestRepoHealthSyncFailedMelga(t *testing.T) {
	s, ts := healthServer(t)
	root := registerRepoAt(t, s, "melga", jiraINI)
	if err := s.stores.Issues().RecordError(root, "linear: issue not found"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}

	_, h := getHealth(t, ts, "melga")
	if h.State != HealthSyncFailed {
		t.Fatalf("state = %q, want sync-failed", h.State)
	}
	if h.LastError != "linear: issue not found" {
		t.Fatalf("last error = %q, want the recorded failure surfaced", h.LastError)
	}
}

// TestRepoHealthInternalShedsStaleError is the filters-demo case: a linear error
// recorded before the wizard wrote TRACKER_PROVIDER=internal. An internal repo's
// issues live in the hub store, so health must read ready — never sync-failed —
// even with the stale error still on its bookkeeping row.
func TestRepoHealthInternalShedsStaleError(t *testing.T) {
	s, ts := healthServer(t)
	root := registerRepoAt(t, s, "filters-demo", internalINI)
	if err := s.stores.Issues().RecordError(root, "linear: no api key"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}

	_, h := getHealth(t, ts, "filters-demo")
	if h.State != HealthReady {
		t.Fatalf("state = %q, want ready for an internal-provider repo", h.State)
	}
}

func TestRepoHealthReady(t *testing.T) {
	s, ts := healthServer(t)
	root := registerRepoAt(t, s, "salonradar", linearINI)
	store := s.stores.Issues()
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "one"},
		{Identifier: "COD-2", Title: "two"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.RecordResult(root, hubstore.SyncResult{
		Issues: 2, Cursor: "2026-07-14T00:00:00Z", SyncedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	_, h := getHealth(t, ts, "salonradar")
	if h.State != HealthReady {
		t.Fatalf("state = %q, want ready after a clean sync", h.State)
	}
	if h.LastSyncedAt == "" || h.LastError != "" || h.IssueCount != 2 {
		t.Fatalf("health = %+v, want a synced time, no error, two issues", h)
	}
}

func TestRepoHealthSyncing(t *testing.T) {
	s, ts := healthServer(t)
	root := registerRepoAt(t, s, "loop", linearINI)
	if !s.syncer.claim(root, time.Now()) {
		t.Fatal("claim did not take; want the repo marked in-flight")
	}

	_, h := getHealth(t, ts, "loop")
	if h.State != HealthSyncing {
		t.Fatalf("state = %q, want syncing while a pull is in flight", h.State)
	}
}

func TestRepoHealthUnknownRepo(t *testing.T) {
	_, ts := healthServer(t)
	code, _ := getHealth(t, ts, "ghost")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", code)
	}
}

func TestRepoHealthRejectsNonGet(t *testing.T) {
	s, ts := healthServer(t)
	registerRepoAt(t, s, "loop", linearINI)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/loop/health", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}

// TestReposAndHealthAgree pins acceptance: the repos list freshness and the
// health endpoint report the same state for the same repo.
func TestReposAndHealthAgree(t *testing.T) {
	s, ts := healthServer(t)
	registerRepoAt(t, s, "fresh", "")
	registerRepoAt(t, s, "loop", linearINI)
	melga := registerRepoAt(t, s, "melga", jiraINI)
	if err := s.stores.Issues().RecordError(melga, "linear: issue not found"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}
	registerRepoAt(t, s, "notes", internalINI)

	views := getRepos(t, ts)
	for _, name := range []string{"fresh", "loop", "melga", "notes"} {
		rv := findRepoView(t, views, name)
		if rv.Freshness == nil {
			t.Fatalf("%s: freshness absent on the repos list", name)
		}
		_, h := getHealth(t, ts, name)
		if rv.Freshness.State != h.State {
			t.Fatalf("%s: repos state %q disagrees with health state %q", name, rv.Freshness.State, h.State)
		}
	}
}
