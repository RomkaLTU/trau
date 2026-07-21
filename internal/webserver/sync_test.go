package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// syncServer builds a hub with one exited repo ("acme"), a Reader factory
// returning fake, and returns the server plus the repo root and issue store so a
// test can drive POST /sync and assert what it wrote.
func syncServer(t *testing.T, fake tracker.Reader) (*httptest.Server, string, *hubstore.Issues) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "LINEAR_TEAM=COD\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) { return fake, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, root, testStoresAt(t, home).Issues()
}

func postSync(t *testing.T, ts *httptest.Server, repo string) (*http.Response, SyncResponse) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo+"/sync", nil)
	var out SyncResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode sync response: %v", err)
		}
	}
	return res, out
}

func syncedFixture() []tracker.SyncedIssue {
	return []tracker.SyncedIssue{
		{
			ID:          "COD-1",
			ExternalID:  "iss-1",
			Title:       "First",
			Description: "Body",
			Status:      "In Progress",
			Group:       tracker.StatusGroupStarted,
			Labels:      []string{"ready-for-agent"},
			Parent:      "COD-9",
			UpdatedAt:   "2026-07-10T12:00:00Z",
			Comments: []tracker.SyncedComment{
				{ExternalID: "c1", Author: "Ada", Body: "looks good"},
			},
		},
	}
}

func TestSyncPullsIssuesAndRecordsOutcome(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts, root, store := syncServer(t, fake)

	res, out := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Issues != 1 || out.Comments != 1 || out.Provider != "linear" || out.SyncedAt == "" {
		t.Fatalf("response = %+v, want 1 issue/1 comment/linear/timestamp", out)
	}

	stored, err := store.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 1 || stored[0].Identifier != "COD-1" || len(stored[0].Comments) != 1 {
		t.Fatalf("store = %+v, want COD-1 with one comment", stored)
	}
	if stored[0].Source != "linear" {
		t.Fatalf("source = %q, want linear", stored[0].Source)
	}

	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastIssues != 1 || st.LastComments != 1 || st.LastSyncedAt == "" || st.Cursor != "2026-07-10T12:00:00Z" {
		t.Fatalf("recorded outcome = %+v, want counts/cursor/timestamp", st)
	}
}

func TestSyncReflectsBlockedByRelations(t *testing.T) {
	blocked := tracker.SyncedIssue{
		ID:        "COD-2",
		Title:     "Dependent",
		Group:     tracker.StatusGroupUnstarted,
		UpdatedAt: "2026-07-10T12:00:00Z",
		BlockedBy: []tracker.SyncedBlocker{{ID: "COD-1"}},
	}
	fake := &fakeReader{synced: append(syncedFixture(), blocked)}
	ts, root, store := syncServer(t, fake)

	for i := 0; i < 2; i++ {
		res, _ := postSync(t, ts, "acme")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("sync %d status = %d, want 200", i, res.StatusCode)
		}
		_ = res.Body.Close()
	}

	blockers, err := store.Blockers(root, "COD-2")
	if err != nil {
		t.Fatalf("Blockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0] != "COD-1" {
		t.Fatalf("blockers = %v, want the pulled blocked-by link reflected once", blockers)
	}
	iss, found, err := store.Find(root, "COD-2")
	if err != nil || !found {
		t.Fatalf("find COD-2: found=%v err=%v", found, err)
	}
	if !iss.Blocked {
		t.Fatalf("blocked = false, want COD-2 held back while COD-1 is unresolved")
	}
}

func TestSyncPersistsIdentity(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture(), identityID: "u-42", identityName: "Grace Hopper"}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if fake.identityCalls != 1 {
		t.Fatalf("Identity called %d times, want 1 per sync cycle", fake.identityCalls)
	}
	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Me.ID != "u-42" || st.Me.Name != "Grace Hopper" || st.Me.ResolvedAt == "" {
		t.Fatalf("me = %+v, want u-42/Grace Hopper resolved", st.Me)
	}
}

func TestSyncSucceedsWhenIdentityFails(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture(), identityErr: errors.New("bad creds")}
	ts, root, store := syncServer(t, fake)

	res, out := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an identity failure must never fail a sync", res.StatusCode)
	}
	if out.Issues != 1 {
		t.Fatalf("issues = %d, want the pull to still land", out.Issues)
	}
	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Me.ID != "" || st.Me.Name != "" {
		t.Fatalf("me = %+v, want empty when the identity call failed", st.Me)
	}
	if st.LastIssues != 1 {
		t.Fatalf("recorded issues = %d, want the sync recorded normally", st.LastIssues)
	}
}

func TestSyncIsIdempotentAndCachesBinding(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts, root, store := syncServer(t, fake)

	for i := 0; i < 2; i++ {
		res, _ := postSync(t, ts, "acme")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("sync %d status = %d, want 200", i, res.StatusCode)
		}
		_ = res.Body.Close()
	}

	stored, err := store.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("issues = %d after two syncs, want 1 (upsert must not duplicate)", len(stored))
	}
	if fake.bindingCalls != 1 {
		t.Fatalf("ResolveBinding called %d times, want 1 (binding must be cached)", fake.bindingCalls)
	}
}

func TestSyncSecondPullIsIncremental(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts, _, _ := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()
	if fake.syncSince != "" {
		t.Fatalf("first pull since = %q, want a full pull", fake.syncSince)
	}

	res, _ = postSync(t, ts, "acme")
	_ = res.Body.Close()
	if fake.syncSince != "2026-07-10T12:00:00Z" {
		t.Fatalf("second pull since = %q, want the stored cursor", fake.syncSince)
	}
}

func TestSyncEmptyIncrementalPullKeepsCursor(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()

	fake.synced = nil
	res, _ = postSync(t, ts, "acme")
	_ = res.Body.Close()

	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Cursor != "2026-07-10T12:00:00Z" {
		t.Fatalf("cursor = %q, want it preserved when nothing changed", st.Cursor)
	}
}

func TestSyncTrackerErrorKeepsLastGoodCursor(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()

	fake.syncErr = errors.New("linear: 500")
	res, _ = postSync(t, ts, "acme")
	_ = res.Body.Close()

	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastError == "" {
		t.Fatalf("state = %+v, want the failure recorded", st)
	}
	if st.Cursor != "2026-07-10T12:00:00Z" || st.LastSyncedAt == "" {
		t.Fatalf("state = %+v, want the last good cursor and synced time preserved", st)
	}
}

func TestSyncUnknownRepo(t *testing.T) {
	ts, _, _ := syncServer(t, &fakeReader{})
	res, _ := postSync(t, ts, "ghost")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestSyncRejectsNonPOST(t *testing.T) {
	ts, _, _ := syncServer(t, &fakeReader{})
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/sync")
	if err != nil {
		t.Fatalf("GET sync: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}

func TestSyncWithoutCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	seedRepo(t, home, "acme")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) { return nil, tracker.ErrReaderUnavailable }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
}

// TestSyncJiraExplicitNoProjectKeyReportsKey is the legacy melga config: an explicit
// jira provider with valid REST creds but no project key. The sync must name the key
// to set — not the credentials, which are fine — and land the repo at sync-failed with
// that same reason rather than a no-credentials state.
func TestSyncJiraExplicitNoProjectKeyReportsKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "TRACKER_PROVIDER=jira\nJIRA_BASE_URL=https://acme.atlassian.net\nJIRA_EMAIL=dev@acme.io\nJIRA_API_TOKEN=tok\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/sync", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg := body["error"]; strings.Contains(msg, "credentials") || !strings.Contains(msg, "LINEAR_TEAM") {
		t.Fatalf("error = %q, want it to name LINEAR_TEAM and not mention credentials", msg)
	}

	repo, ok := s.findRepo("acme")
	if !ok {
		t.Fatal("findRepo acme = false")
	}
	h := s.repoHealth(repo)
	if h.State != HealthSyncFailed {
		t.Fatalf("health state = %q, want sync-failed (misconfigured, not unconfigured)", h.State)
	}
	if strings.Contains(h.LastError, "credentials") || !strings.Contains(h.LastError, "project key") {
		t.Fatalf("health last error = %q, want the missing project key without mentioning credentials", h.LastError)
	}
}

func TestSyncInternalProviderClearsStaleError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "TRACKER_PROVIDER=internal\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	store := testStoresAt(t, home).Issues()
	if err := store.RecordError(root, "linear: no api key"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastError != "" {
		t.Fatalf("last error = %q, want cleared for an explicit internal provider", st.LastError)
	}
}

func TestSyncImplicitInternalKeepsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	store := testStoresAt(t, home).Issues()
	if err := store.RecordError(root, "linear: 500"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
	if st, _ := store.SyncState(root); st.LastError != "linear: 500" {
		t.Fatalf("last error = %q, want the recorded failure kept", st.LastError)
	}
}

func TestSyncTrackerErrorRecordsAndReports(t *testing.T) {
	fake := &fakeReader{syncErr: errors.New("linear: 500")}
	ts, root, store := syncServer(t, fake)

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", res.StatusCode)
	}
	st, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastError == "" {
		t.Fatalf("last error not recorded: %+v", st)
	}
}

func TestRegisterTriggersSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	base := t.TempDir()
	root := gitRepo(t, base, "acme", "dir")

	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) {
		return &fakeReader{synced: syncedFixture()}, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", res.StatusCode)
	}

	stored, err := testStoresAt(t, home).Issues().List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 1 || stored[0].Identifier != "COD-1" {
		t.Fatalf("register did not seed the issue store: %+v", stored)
	}
}
