package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// fakeReader stands in for a direct tracker client so the backlog endpoint is
// asserted without any network.
type fakeReader struct {
	items    []tracker.BacklogItem
	err      error
	issue    tracker.IssueSummary
	issueErr error

	binding      tracker.ProjectBinding
	bindingErr   error
	bindingCalls int
	synced       []tracker.SyncedIssue
	syncErr      error
	syncSince    string
	syncCalls    int
}

func (f *fakeReader) Backlog(context.Context) ([]tracker.BacklogItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f *fakeReader) Issue(context.Context, string) (tracker.IssueSummary, error) {
	if f.issueErr != nil {
		return tracker.IssueSummary{}, f.issueErr
	}
	return f.issue, nil
}

func (f *fakeReader) ResolveBinding(context.Context) (tracker.ProjectBinding, error) {
	f.bindingCalls++
	if f.bindingErr != nil {
		return tracker.ProjectBinding{}, f.bindingErr
	}
	binding := f.binding
	if !binding.Resolved() {
		binding.ProjectID = "proj-1"
	}
	return binding, nil
}

func (f *fakeReader) SyncPull(_ context.Context, _ tracker.ProjectBinding, since string) ([]tracker.SyncedIssue, error) {
	f.syncCalls++
	f.syncSince = since
	if f.syncErr != nil {
		return nil, f.syncErr
	}
	return f.synced, nil
}

// backlogServer builds a hub with one exited repo ("acme") and a Reader factory
// returning fake (or readerErr when set), and returns the server, its test HTTP
// server, the repo root, and an issue store at the same home so a test can seed
// the store and drive GET /backlog. The reader still backs the run-once issue
// endpoint, so its factory is wired even though the backlog now serves the store.
func backlogServer(t *testing.T, fake tracker.Reader, readerErr error) (*Server, *httptest.Server, string, *hubstore.Issues) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.newReader = func(config.Config) (tracker.Reader, error) {
		if readerErr != nil {
			return nil, readerErr
		}
		return fake, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, root, testStoresAt(t, home).Issues()
}

func getBacklog(t *testing.T, ts *httptest.Server, repo string) (*http.Response, BacklogResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/backlog")
	if err != nil {
		t.Fatalf("GET backlog: %v", err)
	}
	var out BacklogResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode backlog: %v", err)
		}
	}
	return res, out
}

func getBacklogQuery(t *testing.T, ts *httptest.Server, repo, query string) BacklogResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/backlog?" + query)
	if err != nil {
		t.Fatalf("GET backlog?%s: %v", query, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for ?%s", res.StatusCode, query)
	}
	var out BacklogResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode backlog: %v", err)
	}
	return out
}

func filterFixture() []hubstore.Issue {
	return []hubstore.Issue{
		{Identifier: "COD-1", Title: "Login epic", Status: "Backlog", StatusGroup: "backlog", Labels: []string{"feature"}},
		{Identifier: "COD-2", Title: "Fix logout bug", Status: "Todo", StatusGroup: "unstarted", Labels: []string{"bug"}},
		{Identifier: "COD-3", Title: "Dashboard polish", Status: "In Progress", StatusGroup: "started", Labels: []string{"feature"}},
	}
}

func idSet(items []BacklogEntry) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestBacklogAppliesQueryFilters(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", filterFixture()); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	if _, _, err := store.Upsert(root, "internal", []hubstore.Issue{
		{Identifier: "COD-9", Title: "Login note", StatusGroup: "unstarted"},
	}); err != nil {
		t.Fatalf("seed internal: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"state group", "state=unstarted", []string{"COD-2", "COD-9"}},
		{"label", "label=feature", []string{"COD-1", "COD-3"}},
		{"source internal", "source=internal", []string{"COD-9"}},
		{"text over id and title", "q=login", []string{"COD-1", "COD-9"}},
		{"filters compose", "source=synced&q=log", []string{"COD-1", "COD-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := getBacklogQuery(t, ts, "acme", tt.query)
			if got := idSet(out.Items); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("items = %v, want %v", got, tt.want)
			}
			if out.Total != len(tt.want) {
				t.Errorf("total = %d, want %d", out.Total, len(tt.want))
			}
		})
	}
}

func TestBacklogPaginatesWithTotal(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", filterFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	first := getBacklogQuery(t, ts, "acme", "limit=2")
	if got := idSet(first.Items); !reflect.DeepEqual(got, []string{"COD-1", "COD-2"}) {
		t.Fatalf("first page = %v, want the first two", got)
	}
	if first.Total != 3 {
		t.Fatalf("total = %d, want the full count of 3", first.Total)
	}

	second := getBacklogQuery(t, ts, "acme", "limit=2&offset=2")
	if got := idSet(second.Items); !reflect.DeepEqual(got, []string{"COD-3"}) {
		t.Fatalf("second page = %v, want the remaining one", got)
	}
}

func TestBacklogDefaultViewUnpaginated(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", filterFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out := getBacklogQuery(t, ts, "acme", "")
	if len(out.Items) != 3 || out.Total != 3 {
		t.Fatalf("default view = %d items, total %d, want all 3", len(out.Items), out.Total)
	}
}

func backlogFixture() []hubstore.Issue {
	return []hubstore.Issue{
		{Identifier: "COD-10", Title: "Epic", Status: "Backlog", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-11", Title: "Child", Status: "Todo", StatusGroup: "unstarted", Parent: "COD-10", Labels: []string{"ready-for-agent"}},
	}
}

func TestBacklogServesStoredIssues(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", backlogFixture()); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	res, out := getBacklog(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Provider != "linear" {
		t.Errorf("provider = %q, want linear (the resolved default)", out.Provider)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(out.Items))
	}
	epic := out.Items[0]
	if epic.ID != "COD-10" || epic.Group != "backlog" || !epic.HasChildren || epic.Source != "linear" {
		t.Errorf("epic entry = %+v, want the COD-10 backlog epic synced from linear", epic)
	}
	if epic.Labels == nil {
		t.Error("labels serialized as null, want an empty array")
	}
	child := out.Items[1]
	if child.Parent != "COD-10" || !child.Ready || child.Group != "unstarted" {
		t.Errorf("child entry = %+v, want ready unstarted child of COD-10", child)
	}
}

func TestBacklogIncludesInternalIssues(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "Synced", Status: "Todo", StatusGroup: "unstarted"},
	}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	if _, _, err := store.Upsert(root, "internal", []hubstore.Issue{
		{Identifier: "COD-2", Title: "Internal", Status: "Todo", StatusGroup: "unstarted"},
	}); err != nil {
		t.Fatalf("seed internal: %v", err)
	}

	res, out := getBacklog(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	sources := map[string]string{}
	for _, it := range out.Items {
		sources[it.ID] = it.Source
	}
	if sources["COD-1"] != "linear" || sources["COD-2"] != "internal" {
		t.Fatalf("sources = %v, want COD-1 linear and COD-2 internal so the board can tell them apart", sources)
	}
}

func TestBacklogReportsFreshness(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", backlogFixture()); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := store.RecordResult(root, hubstore.SyncResult{Issues: 2, SyncedAt: nowStamp()}); err != nil {
		t.Fatalf("record sync: %v", err)
	}

	res, out := getBacklog(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if out.Freshness == nil {
		t.Fatal("freshness absent after a recorded sync")
	}
	if out.Freshness.LastSyncedAt == "" || out.Freshness.LastIssues != 2 {
		t.Fatalf("freshness = %+v, want a synced time and two issues", out.Freshness)
	}
}

func TestBacklogFreshnessAbsentUntilSynced(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, nil)

	res, out := getBacklog(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with an empty store", res.StatusCode)
	}
	if len(out.Items) != 0 {
		t.Fatalf("items = %d, want none before any sync", len(out.Items))
	}
	if out.Freshness != nil {
		t.Fatalf("freshness = %+v, want none before any sync", out.Freshness)
	}
}

func TestBacklogStaleTriggersBackgroundSync(t *testing.T) {
	fake := &fakeReader{synced: syncedFixture()}
	s, ts, root, store := backlogServer(t, fake, nil)
	s.syncer.ctx = context.Background()
	s.syncer.interval = time.Minute

	res, _ := getBacklog(t, ts, "acme")
	_ = res.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		st, err := store.SyncState(root)
		if err != nil {
			t.Fatalf("read sync state: %v", err)
		}
		if st.LastSyncedAt != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a stale backlog read did not trigger a background sync")
		}
		time.Sleep(5 * time.Millisecond)
	}
	items, err := store.Backlog(root)
	if err != nil {
		t.Fatalf("read backlog: %v", err)
	}
	if len(items) != 1 || items[0].Identifier != "COD-1" {
		t.Fatalf("store = %+v, want the issue the background sync pulled", items)
	}
}

func TestBacklogFreshDoesNotResync(t *testing.T) {
	fake := &fakeReader{synced: []tracker.SyncedIssue{
		{ID: "COD-9", Title: "Would only appear from a resync", UpdatedAt: "2026-07-10T12:00:00Z"},
	}}
	s, ts, root, store := backlogServer(t, fake, nil)
	s.syncer.ctx = context.Background()
	s.syncer.interval = time.Minute
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "Already here", Status: "Todo", StatusGroup: "unstarted"},
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := store.RecordResult(root, hubstore.SyncResult{Issues: 1, SyncedAt: nowStamp()}); err != nil {
		t.Fatalf("record sync: %v", err)
	}

	res, out := getBacklog(t, ts, "acme")
	_ = res.Body.Close()
	if len(out.Items) != 1 || out.Items[0].ID != "COD-1" {
		t.Fatalf("items = %+v, want only the freshly-synced COD-1", out.Items)
	}

	time.Sleep(50 * time.Millisecond)
	items, err := store.Backlog(root)
	if err != nil {
		t.Fatalf("read backlog: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("store = %+v, want no resync of a fresh store", items)
	}
}

func TestBacklogUnknownRepo(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, nil)
	res, _ := getBacklog(t, ts, "nope")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestBacklogRejectsNonGET(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/backlog", map[string]string{})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}

func TestBacklogRequiresTokenWhenExposed(t *testing.T) {
	s := New("1.2.3", "0.0.0.0", "s3cret", nil, false, testStores(t))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/backlog")
	if err != nil {
		t.Fatalf("GET backlog: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated backlog = %d, want 401 on an exposed bind", res.StatusCode)
	}
}
