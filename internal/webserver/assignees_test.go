package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

func getAssignees(t *testing.T, ts *httptest.Server, repo string) (*http.Response, AssigneesResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/assignees")
	if err != nil {
		t.Fatalf("GET assignees: %v", err)
	}
	var out AssigneesResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode assignees: %v", err)
		}
	}
	return res, out
}

func seedAssignedBacklog(t *testing.T, store *hubstore.Issues, root string) {
	t.Helper()
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "Ada one", StatusGroup: "backlog", AssigneeID: "u-1", AssigneeName: "Ada"},
		{Identifier: "COD-2", Title: "Ada two", StatusGroup: "unstarted", AssigneeID: "u-1", AssigneeName: "Ada"},
		{Identifier: "COD-3", Title: "Bob one", StatusGroup: "started", AssigneeID: "u-2", AssigneeName: "Bob"},
		{Identifier: "COD-4", Title: "Nobody", StatusGroup: "backlog"},
	}); err != nil {
		t.Fatalf("seed assigned backlog: %v", err)
	}
}

func TestBacklogFiltersByAssignee(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	seedAssignedBacklog(t, store, root)

	if out := getBacklogQuery(t, ts, "acme", "assignee=me"); len(out.Items) != 0 || out.Total != 0 {
		t.Fatalf("assignee=me without a stored identity = %v (total %d), want an empty page", idSet(out.Items), out.Total)
	}

	if err := store.SaveIdentity(root, "u-1", "Ada"); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"me resolves to the stored identity", "assignee=me", []string{"COD-2", "COD-1"}},
		{"explicit id", "assignee=u-2", []string{"COD-3"}},
		{"unassigned", "assignee=unassigned", []string{"COD-4"}},
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

func TestBacklogEntryCarriesAssignee(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	seedAssignedBacklog(t, store, root)
	if err := store.SaveIdentity(root, "u-1", "Ada"); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	res, out := getBacklog(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	byID := map[string]BacklogEntry{}
	for _, it := range out.Items {
		byID[it.ID] = it
	}
	me := byID["COD-1"].Assignee
	if me == nil || me.ID != "u-1" || me.Name != "Ada" || !me.Me {
		t.Fatalf("COD-1 assignee = %+v, want u-1/Ada flagged me", me)
	}
	other := byID["COD-3"].Assignee
	if other == nil || other.ID != "u-2" || other.Me {
		t.Fatalf("COD-3 assignee = %+v, want u-2 not flagged me", other)
	}
	if a := byID["COD-4"].Assignee; a != nil {
		t.Fatalf("COD-4 assignee = %+v, want nil for an unassigned issue", a)
	}

	fields := backlogItemFields(t, ts, "acme")
	raw, ok := fields["COD-4"]["assignee"]
	if !ok {
		t.Fatal("unassigned COD-4 JSON omits assignee, want an explicit null")
	}
	if string(raw) != "null" {
		t.Fatalf("COD-4 assignee JSON = %s, want null", raw)
	}
}

func TestAssigneesFacetEndpoint(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	seedAssignedBacklog(t, store, root)
	if err := store.SaveIdentity(root, "u-2", "Bob"); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	res, out := getAssignees(t, ts, "acme")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	want := []AssigneeFacet{
		{ID: "u-2", Name: "Bob", Count: 1, Me: true},
		{ID: "u-1", Name: "Ada", Count: 2, Me: false},
	}
	if !reflect.DeepEqual(out.Assignees, want) {
		t.Fatalf("assignees = %+v, want the Me row (Bob) pinned first despite its lower count %+v", out.Assignees, want)
	}
	if out.Unassigned != 1 {
		t.Fatalf("unassigned = %d, want 1 (COD-4)", out.Unassigned)
	}
}

func TestIssueCarriesAssignee(t *testing.T) {
	_, ts, root, store := backlogServer(t, nil, nil)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "Assigned", StatusGroup: "unstarted", AssigneeID: "u-1", AssigneeName: "Ada"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.SaveIdentity(root, "u-1", "Ada"); err != nil {
		t.Fatalf("save identity: %v", err)
	}

	res, out := getIssue(t, ts, "acme", "COD-1")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Assignee == nil || out.Assignee.ID != "u-1" || !out.Assignee.Me {
		t.Fatalf("issue assignee = %+v, want u-1 flagged me", out.Assignee)
	}
}

func TestAssigneesUnknownRepo(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, nil)
	res, _ := getAssignees(t, ts, "nope")
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestAssigneesRejectsNonGET(t *testing.T) {
	_, ts, _, _ := backlogServer(t, nil, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/assignees", map[string]string{})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}
