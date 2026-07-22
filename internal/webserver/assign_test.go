package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// assignServer builds a server with one known repo ("acme") on the tracker
// provider named by ini, wired to fake as its direct Writer — a nil fake leaves
// the real factory in place, so a provider with no writer refuses at build time.
func assignServer(t *testing.T, fake tracker.Writer, ini string) (*httptest.Server, string, *hubstore.Issues) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, ini)
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	if fake != nil {
		s.newWriter = func(config.Config) (tracker.Writer, error) { return fake, nil }
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, root, testStoresAt(t, home).Issues()
}

func seedSyncedIssue(t *testing.T, store *hubstore.Issues, root string, iss hubstore.Issue) {
	t.Helper()
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{iss}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
}

func putAssignee(t *testing.T, ts *httptest.Server, id string, body AssignRequest) (*http.Response, IssueResponse) {
	t.Helper()
	res := putJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/"+id+"/assignee", body)
	var out IssueResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode issue: %v", err)
		}
	}
	return res, out
}

func TestAssignIssueWritesTrackerThenMirrors(t *testing.T) {
	fake := newFakeWriter()
	ts, root, store := assignServer(t, fake, "LINEAR_TEAM=COD\n")
	seedSyncedIssue(t, store, root, hubstore.Issue{Identifier: "COD-1", Title: "Fix", StatusGroup: "unstarted"})
	if err := store.SaveIdentity(root, "u-1", "Ada"); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	res, out := putAssignee(t, ts, "COD-1", AssignRequest{ID: "u-1", Name: "Ada"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(fake.assigned) != 1 || fake.assigned[0] != (assignCall{id: "COD-1", assigneeID: "u-1"}) {
		t.Fatalf("tracker writes = %+v, want one assignment of COD-1 to u-1", fake.assigned)
	}
	if out.Assignee == nil || out.Assignee.ID != "u-1" || out.Assignee.Name != "Ada" || !out.Assignee.Me {
		t.Fatalf("response assignee = %+v, want u-1/Ada flagged me", out.Assignee)
	}
	iss, _, err := store.Find(root, "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.AssigneeID != "u-1" || iss.AssigneeName != "Ada" {
		t.Fatalf("stored assignee = %q/%q, want the mirrored u-1/Ada", iss.AssigneeID, iss.AssigneeName)
	}
}

func TestAssignIssueUnassigns(t *testing.T) {
	fake := newFakeWriter()
	ts, root, store := assignServer(t, fake, "LINEAR_TEAM=COD\n")
	seedSyncedIssue(t, store, root, hubstore.Issue{
		Identifier: "COD-1", Title: "Fix", StatusGroup: "unstarted",
		AssigneeID: "u-1", AssigneeName: "Ada",
	})

	res, out := putAssignee(t, ts, "COD-1", AssignRequest{Name: "Ada"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(fake.assigned) != 1 || fake.assigned[0].assigneeID != "" {
		t.Fatalf("tracker writes = %+v, want one clearing assignment", fake.assigned)
	}
	if out.Assignee != nil {
		t.Fatalf("response assignee = %+v, want null for an unassigned issue", out.Assignee)
	}
	iss, _, err := store.Find(root, "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.AssigneeID != "" || iss.AssigneeName != "" {
		t.Fatalf("stored assignee = %q/%q, want it cleared — a stale name must not linger", iss.AssigneeID, iss.AssigneeName)
	}
}

// The tracker is the authority: a refused write must leave the stored row exactly
// as it was, so the board never shows an assignment that does not exist upstream.
func TestAssignIssueTrackerFailureLeavesStoreUntouched(t *testing.T) {
	fake := newFakeWriter()
	fake.assignErr = errors.New("linear: unauthorized")
	ts, root, store := assignServer(t, fake, "LINEAR_TEAM=COD\n")
	seedSyncedIssue(t, store, root, hubstore.Issue{
		Identifier: "COD-1", Title: "Fix", StatusGroup: "unstarted",
		AssigneeID: "u-1", AssigneeName: "Ada",
	})

	res, _ := putAssignee(t, ts, "COD-1", AssignRequest{ID: "u-2", Name: "Bob"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when the tracker refuses the write", res.StatusCode)
	}
	iss, _, err := store.Find(root, "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.AssigneeID != "u-1" || iss.AssigneeName != "Ada" {
		t.Fatalf("stored assignee = %q/%q, want the original u-1/Ada", iss.AssigneeID, iss.AssigneeName)
	}
}

func TestAssignEndpointsRefuseInternalProvider(t *testing.T) {
	fake := newFakeWriter()
	ts, root, store := assignServer(t, fake, "TRACKER_PROVIDER=internal\n")
	seedSyncedIssue(t, store, root, hubstore.Issue{Identifier: "COD-1", Title: "Fix", StatusGroup: "unstarted"})

	assign, _ := putAssignee(t, ts, "COD-1", AssignRequest{ID: "u-1", Name: "Ada"})
	_ = assign.Body.Close()
	if assign.StatusCode != http.StatusConflict {
		t.Fatalf("assign status = %d, want 409 for a provider with no assignment API", assign.StatusCode)
	}
	users, err := http.Get(ts.URL + APIPrefix + "/repos/acme/assignable-users")
	if err != nil {
		t.Fatalf("GET assignable-users: %v", err)
	}
	_ = users.Body.Close()
	if users.StatusCode != http.StatusConflict {
		t.Fatalf("assignable-users status = %d, want 409", users.StatusCode)
	}
	if len(fake.assigned) != 0 {
		t.Errorf("tracker writes = %+v, want none for an internal repo", fake.assigned)
	}
	iss, _, err := store.Find(root, "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.AssigneeID != "" {
		t.Errorf("stored assignee = %q, want internal issues to stay Unassigned", iss.AssigneeID)
	}
}

// GitHub has no direct-API writer at all, so the refusal comes from the writer
// build rather than the call, and must still read as the same capability gap.
func TestAssignEndpointsRefuseGitHubProvider(t *testing.T) {
	ts, root, store := assignServer(t, nil, "TRACKER_PROVIDER=github\n")
	seedSyncedIssue(t, store, root, hubstore.Issue{Identifier: "COD-1", Title: "Fix", StatusGroup: "unstarted"})

	assign, _ := putAssignee(t, ts, "COD-1", AssignRequest{ID: "u-1", Name: "Ada"})
	_ = assign.Body.Close()
	if assign.StatusCode != http.StatusConflict {
		t.Fatalf("assign status = %d, want 409 for a provider with no assignment API", assign.StatusCode)
	}
	users, err := http.Get(ts.URL + APIPrefix + "/repos/acme/assignable-users")
	if err != nil {
		t.Fatalf("GET assignable-users: %v", err)
	}
	_ = users.Body.Close()
	if users.StatusCode != http.StatusConflict {
		t.Fatalf("assignable-users status = %d, want 409", users.StatusCode)
	}
	iss, _, err := store.Find(root, "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.AssigneeID != "" {
		t.Errorf("stored assignee = %q, want nothing persisted", iss.AssigneeID)
	}
}

func TestAssignableUsersFlagsMe(t *testing.T) {
	fake := newFakeWriter()
	fake.users = []tracker.AssignableUser{
		{ID: "u-1", Name: "Ada"},
		{ID: "u-2", Name: "Bob"},
		{ID: "u-3", Name: "Cy"},
	}
	ts, root, store := assignServer(t, fake, "LINEAR_TEAM=COD\n")
	if err := store.SaveIdentity(root, "u-2", "Bob"); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/assignable-users?query=a")
	if err != nil {
		t.Fatalf("GET assignable-users: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out AssignableUsersResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode assignable users: %v", err)
	}
	want := []AssignableUser{
		{ID: "u-2", Name: "Bob", Me: true},
		{ID: "u-1", Name: "Ada"},
		{ID: "u-3", Name: "Cy"},
	}
	if !reflect.DeepEqual(out.Users, want) {
		t.Fatalf("users = %+v, want the Me row pinned first %+v", out.Users, want)
	}
	if len(fake.userQueries) != 1 || fake.userQueries[0] != "a" {
		t.Errorf("lookup queries = %v, want the request's query passed through", fake.userQueries)
	}
}
