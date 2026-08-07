package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
)

// JIRA_BOARD_STATES is an overlay, not the exhaustive mapping its Azure
// counterpart is: a status it names takes the mapped section, and a status it does
// not keeps whatever its Jira status category derives — including a status created
// after the mapping was written, which is the whole reason for the difference.
func TestJiraBoardStatesOverlaysTheCategoryGrouping(t *testing.T) {
	mapping := parseJiraBoardStates("Backlog=backlog, ready for qa =started,Closed=done")
	cases := []struct {
		name                         string
		status, category, resolution string
		want                         StatusGroup
		wantOverride                 bool
	}{
		{name: "mapped status", status: "Backlog", category: "new", want: StatusGroupBacklog, wantOverride: true},
		{name: "mapped case- and space-insensitively", status: "  Ready For QA  ", category: "indeterminate", want: StatusGroupStarted, wantOverride: true},
		{name: "unlisted status keeps its category", status: "In Progress", category: "indeterminate", want: StatusGroupStarted},
		{name: "status added later keeps its category, not unknown", status: "Blocked", category: "new", want: StatusGroupUnstarted},
		{name: "unlisted done status", status: "Shipped", category: "done", want: StatusGroupDone},
		{name: "the won't-do nuance survives on an unlisted status", status: "Shipped", category: "done", resolution: "Won't Do", want: StatusGroupCanceled},
		{name: "a duplicate resolution on an unlisted status", status: "Shipped", category: "done", resolution: "Duplicate", want: StatusGroupCanceled},
		{name: "an explicit mapping overrides the resolution nuance", status: "Closed", category: "done", resolution: "Won't Do", want: StatusGroupDone, wantOverride: true},
		{name: "Jira's No Category status still lands in a section", status: "Icebox", category: "undefined", want: StatusGroupUnstarted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapping.group(tc.status, tc.category, tc.resolution); got != tc.want {
				t.Errorf("group(%q, %q, %q) = %q, want %q", tc.status, tc.category, tc.resolution, got, tc.want)
			}
			if _, ok := mapping.override(tc.status); ok != tc.wantOverride {
				t.Errorf("override(%q) mapped = %v, want %v", tc.status, ok, tc.wantOverride)
			}
		})
	}
}

// An empty or unparseable spec leaves grouping exactly where it was, so deleting
// the key restores pure category-derived grouping.
func TestJiraBoardStatesEmptyLeavesCategoryGrouping(t *testing.T) {
	for _, spec := range []string{"", "   ", ",,", "Backlog=nowhere", "=started"} {
		mapping := parseJiraBoardStates(spec)
		if _, ok := mapping.override("Backlog"); ok {
			t.Errorf("parseJiraBoardStates(%q) claims an override for Backlog", spec)
		}
		if got := mapping.group("Backlog", "new", ""); got != StatusGroupUnstarted {
			t.Errorf("parseJiraBoardStates(%q): group = %q, want %q", spec, got, StatusGroupUnstarted)
		}
	}
}

// Two repos on one Jira project share a pull only when they group its statuses
// the same way, so the mapping's canonical key has to differ whenever the mapping
// does — and match whatever order or casing it was written in.
func TestJiraScopeKeyDivergesWithTheMapping(t *testing.T) {
	binding := ProjectBinding{ProjectID: "PROJ", Project: "PROJ"}
	key := func(spec string) string {
		r := &jiraReader{baseURL: "https://acme.atlassian.net", boardStates: parseJiraBoardStates(spec)}
		return r.scopeKey("pull", binding, "")
	}

	same := key("Backlog=backlog,Ready for QA=started")
	reordered := key("ready for qa=started , Backlog=backlog")
	if same != reordered {
		t.Errorf("the same mapping written twice keys differently:\n%q\n%q", same, reordered)
	}
	if other := key("Backlog=unstarted,Ready for QA=started"); other == same {
		t.Errorf("two different mappings share the pull key %q", same)
	}
	if unmapped := key(""); unmapped == same {
		t.Errorf("an unmapped repo shares the pull key of a mapped one (%q)", same)
	}
}

// Two Jira sites that happen to share a project key must not share a pull either:
// the key is the site plus the project, not the project alone.
func TestJiraScopeKeySeparatesSites(t *testing.T) {
	binding := ProjectBinding{ProjectID: "PROJ"}
	acme := (&jiraReader{baseURL: "https://acme.atlassian.net"}).scopeKey("pull", binding, "")
	widgets := (&jiraReader{baseURL: "https://widgets.atlassian.net"}).scopeKey("pull", binding, "")
	if acme == widgets {
		t.Errorf("two Jira sites share the pull key %q", acme)
	}
}

// jiraBacklogFake answers the backlog search with a project whose Closed status is
// reached both by a genuine completion and by a duplicate resolution, plus a
// Ready for QA status Jira types indeterminate.
func jiraBacklogFake(t *testing.T) *httptest.Server {
	t.Helper()
	const payload = `{"issues":[
		{"key":"PROJ-1","fields":{
			"summary":"queued","status":{"name":"Ready for QA","statusCategory":{"key":"indeterminate"}},
			"issuetype":{"hierarchyLevel":0}}},
		{"key":"PROJ-2","fields":{
			"summary":"running","status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
			"issuetype":{"hierarchyLevel":0}}},
		{"key":"PROJ-3","fields":{
			"summary":"parked","status":{"name":"To Do","statusCategory":{"key":"new"}},
			"issuetype":{"hierarchyLevel":0}}},
		{"key":"PROJ-4","fields":{
			"summary":"redundant","status":{"name":"Closed","statusCategory":{"key":"done"}},
			"issuetype":{"hierarchyLevel":0},"resolution":{"name":"Duplicate"}}},
		{"key":"PROJ-5","fields":{
			"summary":"shipped","status":{"name":"Closed","statusCategory":{"key":"done"}},
			"issuetype":{"hierarchyLevel":0},"resolution":{"name":"Done"}}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The board a repo reads follows its overlay: the one status it remaps moves, and
// every other row stays exactly where its category put it — the won't-do nuance
// included, until the mapping names the status that carries it.
func TestJiraReaderBacklogAppliesTheOverlay(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want map[string]StatusGroup
	}{
		{
			name: "no mapping keeps pure category grouping",
			want: map[string]StatusGroup{
				"PROJ-1": StatusGroupStarted,
				"PROJ-2": StatusGroupStarted,
				"PROJ-3": StatusGroupUnstarted,
				"PROJ-4": StatusGroupCanceled,
				"PROJ-5": StatusGroupDone,
			},
		},
		{
			name: "one override moves one status and leaves the rest alone",
			spec: "Ready for QA=done",
			want: map[string]StatusGroup{
				"PROJ-1": StatusGroupDone,
				"PROJ-2": StatusGroupStarted,
				"PROJ-3": StatusGroupUnstarted,
				"PROJ-4": StatusGroupCanceled,
				"PROJ-5": StatusGroupDone,
			},
		},
		{
			name: "mapping the done status displaces the duplicate-resolution nuance",
			spec: "Closed=done",
			want: map[string]StatusGroup{
				"PROJ-1": StatusGroupStarted,
				"PROJ-2": StatusGroupStarted,
				"PROJ-3": StatusGroupUnstarted,
				"PROJ-4": StatusGroupDone,
				"PROJ-5": StatusGroupDone,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := jiraBacklogFake(t)
			r := &jiraReader{
				client:      jiraapi.New(srv.URL, "me@acme.com", "tok"),
				baseURL:     srv.URL,
				project:     "PROJ",
				boardStates: parseJiraBoardStates(tc.spec),
			}
			items, err := r.Backlog(context.Background())
			if err != nil {
				t.Fatalf("Backlog error: %v", err)
			}
			if len(items) != len(tc.want) {
				t.Fatalf("read %d rows, want %d", len(items), len(tc.want))
			}
			for _, item := range items {
				if got := item.Group; got != tc.want[item.ID] {
					t.Errorf("%s (%s) grouped as %q, want %q", item.ID, item.Status, got, tc.want[item.ID])
				}
			}
		})
	}
}

// Reconcile reads the same overlay the board groups by, so a --status pass and
// the board never disagree about the same ticket. A status the overlay does not
// name keeps its category's own reading, resolution nuance included.
func TestJiraIssueStatusFollowsTheOverlay(t *testing.T) {
	cases := []struct {
		name                         string
		spec                         string
		status, category, resolution string
		want                         IssueStatus
	}{
		{name: "overridden to done", spec: "Ready for QA=done", status: "Ready for QA", category: "indeterminate", want: StatusDone},
		{name: "overridden to backlog", spec: "Ready for QA=backlog", status: "Ready for QA", category: "indeterminate", want: StatusOpen},
		{name: "unlisted status keeps its category", spec: "To Do=backlog", status: "Ready for QA", category: "indeterminate", want: StatusStarted},
		{name: "no mapping at all", status: "Ready for QA", category: "indeterminate", want: StatusStarted},
		{name: "the duplicate resolution still cancels an unlisted status", status: "Closed", category: "done", resolution: "Duplicate", want: StatusCanceled},
		{name: "an explicit mapping overrides the resolution nuance", spec: "Closed=done", status: "Closed", category: "done", resolution: "Duplicate", want: StatusDone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolution := "null"
			if tc.resolution != "" {
				resolution = `{"name":"` + tc.resolution + `"}`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"key":"PROJ-7","fields":{"summary":"t",
					"status":{"name":"` + tc.status + `","statusCategory":{"key":"` + tc.category + `"}},
					"resolution":` + resolution + `,"project":{"key":"PROJ"},
					"issuetype":{"hierarchyLevel":0}}}`))
			}))
			defer srv.Close()

			j := &Jira{
				Team:        "PROJ",
				BaseURL:     srv.URL,
				Email:       "me@acme.com",
				APIToken:    "tok",
				boardStates: parseJiraBoardStates(tc.spec),
			}
			got, err := j.IssueStatus(context.Background(), "PROJ-7")
			if err != nil {
				t.Fatalf("IssueStatus error: %v", err)
			}
			if got != tc.want {
				t.Errorf("IssueStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// The editor's choices come from the project's own workflows: every status is both
// a groupable row prefilled with its category-derived section and a pin a STATUS_*
// key may name. The suggestion is static — it reads the category alone, because a
// resolution belongs to an issue and not to the status standing for all of them.
func TestJiraStatusOptionsListsTheProjectStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"1","name":"Story","statuses":[
				{"name":"To Do","statusCategory":{"key":"new"}},
				{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
				{"name":"Closed","statusCategory":{"key":"done"}}]},
			{"id":"2","name":"Bug","statuses":[
				{"name":"To Do","statusCategory":{"key":"new"}},
				{"name":"Ready for QA","statusCategory":{"key":"indeterminate"}}]}]`))
	}))
	defer srv.Close()

	opts, err := JiraStatusOptions(context.Background(), Config{
		Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIKey: "tok",
	})
	if err != nil {
		t.Fatalf("JiraStatusOptions error: %v", err)
	}
	wantColumns := []BoardColumnSuggestion{
		{Name: "To Do", SuggestedGroup: StatusGroupUnstarted},
		{Name: "In Progress", SuggestedGroup: StatusGroupStarted},
		{Name: "Closed", SuggestedGroup: StatusGroupDone},
		{Name: "Ready for QA", SuggestedGroup: StatusGroupStarted},
	}
	if len(opts.Columns) != len(wantColumns) {
		t.Fatalf("read %d groupable statuses, want %d", len(opts.Columns), len(wantColumns))
	}
	for i, want := range wantColumns {
		if opts.Columns[i] != want {
			t.Errorf("column %d = %+v, want %+v", i, opts.Columns[i], want)
		}
		if opts.Columns[i].SuggestedGroup == StatusGroupUnknown {
			t.Errorf("column %d suggests Unknown; the editor never offers it", i)
		}
	}
	wantPins := []WorkflowOption{
		{Name: "To Do", Category: "new"},
		{Name: "In Progress", Category: "indeterminate"},
		{Name: "Closed", Category: "done"},
		{Name: "Ready for QA", Category: "indeterminate"},
	}
	for i, want := range wantPins {
		if opts.Pins[i] != want {
			t.Errorf("pin %d = %+v, want %+v", i, opts.Pins[i], want)
		}
	}
}

func TestJiraStatusOptionsNeedsAProject(t *testing.T) {
	cfg := Config{BaseURL: "https://acme.atlassian.net", Email: "me@acme.com", APIKey: "tok"}
	if _, err := JiraStatusOptions(context.Background(), cfg); err == nil {
		t.Fatal("JiraStatusOptions accepted a repo with no project key")
	}
}
