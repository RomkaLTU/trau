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
	"github.com/RomkaLTU/trau/internal/queue"
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
	if err := store.RecordError(root, "linear: no api key", string(tracker.ErrorConfig)); err != nil {
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
	if err := store.RecordError(root, "linear: 500", string(tracker.ErrorConfig)); err != nil {
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

// TestSyncReconcilesTrackerRemovals pins the manual path's promise: after a click
// the board matches the tracker, so a ticket deleted upstream is tombstoned,
// dropped from the queue, and counted in the response.
func TestSyncReconcilesTrackerRemovals(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{syncedIssue("COD-1"), syncedIssue("COD-2")}}
	s, root := reconcileServer(t, fake)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	seed, _ := postSync(t, ts, "acme")
	_ = seed.Body.Close()
	for _, id := range []string{"COD-1", "COD-2"} {
		if _, err := s.stores.Queue(root).Add(queue.Item{Kind: queue.KindTicket, ID: id}); err != nil {
			t.Fatalf("queue %s: %v", id, err)
		}
	}

	fake.identifiers = []string{"COD-2"}
	res, out := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Removed != 1 {
		t.Fatalf("removed = %d, want the one issue that left the tracker", out.Removed)
	}
	if deletedAt(t, s, root, "COD-1") == "" {
		t.Fatal("a manual sync should tombstone the issue the tracker no longer returns")
	}
	items, err := s.stores.Queue(root).Load()
	if err != nil {
		t.Fatalf("queue load: %v", err)
	}
	if len(items) != 1 || items[0].ID != "COD-2" {
		t.Fatalf("queue = %+v, want only COD-2 left", items)
	}
}

func TestSyncReconcileFailureIsNotASuccess(t *testing.T) {
	fake := &fakeReader{
		synced:         []tracker.SyncedIssue{syncedIssue("COD-1")},
		identifiersErr: errors.New("linear: 500"),
	}
	s, root := reconcileServer(t, fake)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 — a half-done sync is an error", res.StatusCode)
	}
	if deletedAt(t, s, root, "COD-1") != "" {
		t.Fatal("a failed sweep must not tombstone anything")
	}
}

func TestSyncInternalProviderSweepsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "TRACKER_PROVIDER=internal\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	if _, _, err := s.stores.Issues().Upsert(root, "linear", []hubstore.Issue{{Identifier: "COD-1", Title: "kept"}}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	res, _ := postSync(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.StatusCode)
	}
	if deletedAt(t, s, root, "COD-1") != "" {
		t.Fatal("a sync refused for lack of a tracker must not sweep the store")
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

// A machine-wide LINEAR_API_KEY makes a repo that configured no tracker resolve to
// linear. Its sync must refuse before any request leaves the machine and record
// what to set, not the not-found a lookup with an empty team key answers with.
func TestSyncRefusesLinearWithoutTeamKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeRepoINI(t, home, "LINEAR_API_KEY=user-linear-key\n")
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "PROJECT=Trau Web\n")

	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, _ := postSync(t, ts, "acme")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 — a missing team key is a config state", res.StatusCode)
	}

	st, err := testStoresAt(t, home).Issues().SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	for _, want := range []string{"LINEAR_TEAM", "TRACKER_PROVIDER=internal"} {
		if !strings.Contains(st.LastError, want) {
			t.Fatalf("last error = %q, want it to mention %q", st.LastError, want)
		}
	}
	if strings.Contains(st.LastError, "not found") {
		t.Fatalf("last error = %q, want the configuration hint rather than the tracker's own error", st.LastError)
	}
	if st.LastErrorKind != string(tracker.ErrorConfig) {
		t.Fatalf("last error kind = %q, want %q", st.LastErrorKind, tracker.ErrorConfig)
	}
}

// The guard is about having nothing to bind with, not about the config key: a repo
// whose stored binding already carries a team id keeps syncing on it.
func TestSyncKeepsStoredBindingWithoutTeamKey(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture(), bindingErr: tracker.ErrNoTeamKey}
	ts, root, store := syncServer(t, fake)
	if err := store.SaveBinding(root, hubstore.SyncBinding{TeamID: "team-1"}); err != nil {
		t.Fatalf("SaveBinding: %v", err)
	}

	res, out := postSync(t, ts, "acme")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the stored binding is usable", res.StatusCode)
	}
	if fake.bindingCalls != 0 {
		t.Fatalf("ResolveBinding called %d times, want the stored binding used as-is", fake.bindingCalls)
	}
	if out.Issues != 1 {
		t.Fatalf("issues = %d, want the pull to have run", out.Issues)
	}
}
