package tracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
)

func TestJiraShouldFallback(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil never falls back", nil, false},
		{"not enabled falls back to MCP", jiraapi.ErrNotEnabled, true},
		{"unauthorized falls back to MCP", jiraapi.ErrUnauthorized, true},
		{"wrapped unauthorized still falls back", fmt.Errorf("title: %w", jiraapi.ErrUnauthorized), true},
		{"not found is surfaced", jiraapi.ErrNotFound, false},
		{"generic error is surfaced", errors.New("boom"), false},
	}
	for _, tc := range tests {
		if got := jiraShouldFallback(tc.err); got != tc.want {
			t.Errorf("%s: jiraShouldFallback(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// With no API token the direct path is disabled (ErrNotEnabled), so Title must
// fall back to the MCP runner and parse its TITLE= sentinel.
func TestJiraTitleFallsBackToRunner(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"title": {Final: "TITLE=Fix the widget"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	got, err := j.Title(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("Title returned error: %v", err)
	}
	if got != "Fix the widget" {
		t.Errorf("Title = %q, want %q", got, "Fix the widget")
	}
	if runner.calls["title"] != 1 {
		t.Errorf("expected exactly one MCP title lookup, got %d", runner.calls["title"])
	}
}

// With a token set, Title resolves via the REST API and never touches the runner.
func TestJiraTitleUsesAPIWhenTokenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-7","fields":{"summary":"Fix the widget"}}`))
	}))
	defer srv.Close()

	runner := &recordingRunner{responses: map[string]agent.Result{}}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	got, err := j.Title(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("Title returned error: %v", err)
	}
	if got != "Fix the widget" {
		t.Errorf("Title = %q, want %q", got, "Fix the widget")
	}
	if runner.calls["title"] != 0 {
		t.Errorf("expected no MCP fallback when the API answers, got %d title calls", runner.calls["title"])
	}
}

func jiraIssueServer(payload string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
}

// With a token set, ListTeams enumerates projects via /project/search and maps
// them onto teams without touching the runner.
func TestJiraListTeamsUsesAPIWhenTokenSet(t *testing.T) {
	srv := jiraIssueServer(`{"values":[{"key":"PROJ","name":"Project X","id":"1"},{"key":"OPS","name":"Operations","id":"2"}],"startAt":0,"maxResults":50,"total":2,"isLast":true}`)
	defer srv.Close()

	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	teams, err := j.ListTeams(context.Background())
	if err != nil {
		t.Fatalf("ListTeams error: %v", err)
	}
	want := []Team{{Key: "PROJ", Name: "Project X"}, {Key: "OPS", Name: "Operations"}}
	if len(teams) != len(want) {
		t.Fatalf("teams = %+v, want %+v", teams, want)
	}
	for i, tm := range want {
		if teams[i] != tm {
			t.Errorf("team[%d] = %+v, want %+v", i, teams[i], tm)
		}
	}
	if runner.calls["list_teams"] != 0 {
		t.Errorf("expected no MCP fallback when the API answers, got %d list_teams calls", runner.calls["list_teams"])
	}
}

// With no token the direct path is disabled (ErrNotEnabled), so ListTeams falls
// back to the MCP runner and parses its TEAMS= sentinel.
func TestJiraListTeamsFallsBackToRunner(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"list_teams": {Final: `TEAMS=[{"key":"PROJ","name":"Project X"}]`},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	teams, err := j.ListTeams(context.Background())
	if err != nil {
		t.Fatalf("ListTeams error: %v", err)
	}
	if len(teams) != 1 || teams[0].Key != "PROJ" || teams[0].Name != "Project X" {
		t.Errorf("teams = %+v, want [PROJ/Project X]", teams)
	}
	if runner.calls["list_teams"] != 1 {
		t.Errorf("expected exactly one MCP list_teams call, got %d", runner.calls["list_teams"])
	}
}

// Onboarding detection builds the tracker with per-repo REST credentials and no
// MCP runner. A rejected token must surface as ErrUnauthorized — never a silent
// fallback to the shared Rovo MCP (a different Atlassian identity) — and the nil
// runner must not panic.
func TestJiraListTeamsRESTOnlySurfacesAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	j := &Jira{Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "bad"}
	if _, err := j.ListTeams(context.Background()); !errors.Is(err, jiraapi.ErrUnauthorized) {
		t.Fatalf("ListTeams err = %v, want ErrUnauthorized", err)
	}
}

// With valid REST credentials and no MCP runner, detection returns the token
// account's projects directly and never needs the runner.
func TestJiraListTeamsRESTOnlySucceeds(t *testing.T) {
	srv := jiraIssueServer(`{"values":[{"key":"VAI","name":"Vaiva","id":"1"}],"startAt":0,"maxResults":50,"total":1,"isLast":true}`)
	defer srv.Close()

	j := &Jira{Team: "VAI", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}
	teams, err := j.ListTeams(context.Background())
	if err != nil {
		t.Fatalf("ListTeams error: %v", err)
	}
	if len(teams) != 1 || teams[0].Key != "VAI" {
		t.Fatalf("teams = %+v, want [VAI]", teams)
	}
}

// mapJiraStatus is the load-bearing mapping the ACs call out: statusCategory →
// open/done/unknown, with a done-category resolution name flipping to canceled.
func TestMapJiraStatus(t *testing.T) {
	cases := []struct {
		name       string
		category   string
		resolution string
		want       IssueStatus
	}{
		{"backlog is open", "new", "", StatusOpen},
		{"in progress is started", "indeterminate", "", StatusStarted},
		{"done resolved is done", "done", "Done", StatusDone},
		{"done unresolved is done", "done", "", StatusDone},
		{"done wont-do is canceled", "done", "Won't Do", StatusCanceled},
		{"done duplicate is canceled", "done", "Duplicate", StatusCanceled},
		{"done declined is canceled", "done", "Declined", StatusCanceled},
		{"resolution match is case-insensitive", "done", "CANCELLED", StatusCanceled},
		{"empty category is unknown", "", "", StatusUnknown},
		{"unrecognized category is unknown", "mystery", "", StatusUnknown},
	}
	for _, tc := range cases {
		if got := mapJiraStatus(tc.category, tc.resolution); got != tc.want {
			t.Errorf("%s: mapJiraStatus(%q, %q) = %q, want %q", tc.name, tc.category, tc.resolution, got, tc.want)
		}
	}
}

func TestJiraIssueStatusUsesAPI(t *testing.T) {
	srv := jiraIssueServer(`{"key":"PROJ-7","fields":{"status":{"name":"Done","statusCategory":{"key":"done"}},"resolution":{"name":"Done"}}}`)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	st, err := j.IssueStatus(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("IssueStatus error: %v", err)
	}
	if st != StatusDone {
		t.Errorf("IssueStatus = %q, want done", st)
	}
	if runner.calls["status"] != 0 {
		t.Errorf("expected no MCP fallback, got %d status calls", runner.calls["status"])
	}
}

func TestJiraIssueStatusFallsBack(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"status": {Final: "STATUS=canceled"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	st, err := j.IssueStatus(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("IssueStatus error: %v", err)
	}
	if st != StatusCanceled {
		t.Errorf("IssueStatus = %q, want canceled", st)
	}
	if runner.calls["status"] != 1 {
		t.Errorf("expected one MCP fallback, got %d status calls", runner.calls["status"])
	}
}

func TestJiraIssueProjectUsesAPI(t *testing.T) {
	srv := jiraIssueServer(`{"key":"PROJ-7","fields":{"project":{"key":"PROJ","name":"Project X","id":"1"}}}`)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	got, err := j.IssueProject(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("IssueProject error: %v", err)
	}
	if got != "PROJ" {
		t.Errorf("IssueProject = %q, want PROJ (project key)", got)
	}
	if runner.calls["project"] != 0 {
		t.Errorf("expected no MCP fallback, got %d project calls", runner.calls["project"])
	}
}

func TestJiraIssueProjectFallsBack(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"project": {Final: "PROJECT=PROJ"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	got, err := j.IssueProject(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("IssueProject error: %v", err)
	}
	if got != "PROJ" {
		t.Errorf("IssueProject = %q, want PROJ", got)
	}
	if runner.calls["project"] != 1 {
		t.Errorf("expected one MCP fallback, got %d project calls", runner.calls["project"])
	}
}

func TestJiraParentIssueUsesAPI(t *testing.T) {
	srv := jiraIssueServer(`{"key":"PROJ-7","fields":{"parent":{"key":"PROJ-1"}}}`)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	got, err := j.ParentIssue(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("ParentIssue error: %v", err)
	}
	if got != "PROJ-1" {
		t.Errorf("ParentIssue = %q, want PROJ-1", got)
	}
	if runner.calls["parent"] != 0 {
		t.Errorf("expected no MCP fallback, got %d parent calls", runner.calls["parent"])
	}
}

func TestJiraParentIssueFallsBack(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"parent": {Final: "PARENT=NONE"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	got, err := j.ParentIssue(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("ParentIssue error: %v", err)
	}
	if got != "" {
		t.Errorf("ParentIssue = %q, want empty (no parent)", got)
	}
	if runner.calls["parent"] != 1 {
		t.Errorf("expected one MCP fallback, got %d parent calls", runner.calls["parent"])
	}
}

func TestJiraIssueDetailUsesAPI(t *testing.T) {
	srv := jiraIssueServer(`{"key":"PROJ-7","fields":{"summary":"Fix the widget","description":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Do X then Y."}]}]}}}`)
	defer srv.Close()
	j := &Jira{Runner: &recordingRunner{}, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	detail, err := j.IssueDetail(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("IssueDetail error: %v", err)
	}
	if detail.Title != "Fix the widget" {
		t.Errorf("Title = %q, want %q", detail.Title, "Fix the widget")
	}
	if detail.Description != "Do X then Y." {
		t.Errorf("Description = %q, want %q", detail.Description, "Do X then Y.")
	}
}

// Without a token IssueDetail is API-only (no MCP fallback), so it surfaces the
// not-enabled error and the pipeline builds without the injected ticket context.
func TestJiraIssueDetailNoTokenErrors(t *testing.T) {
	j := &Jira{Runner: &recordingRunner{}, Team: "PROJ"}
	if _, err := j.IssueDetail(context.Background(), "PROJ-7"); err == nil {
		t.Fatal("IssueDetail without a token should error, got nil")
	}
}

// eligiblePayload lists an epic, a blocked ticket, then a clean leaf — in JQL
// (rank) order — so a picker must skip the first two and land on the leaf.
const eligiblePayload = `{"issues":[
	{"key":"PROJ-2","fields":{"summary":"Epic","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":1},"issuelinks":[]}},
	{"key":"PROJ-3","fields":{"summary":"Blocked","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"issuelinks":[{"type":{"name":"Blocks","inward":"is blocked by"},"inwardIssue":{"key":"PROJ-8","fields":{"status":{"statusCategory":{"key":"new"}}}}}]}},
	{"key":"PROJ-1","fields":{"summary":"Do it","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"issuelinks":[]}}
]}`

func TestJiraPickUsesAPIAndSkipsEpicAndBlocked(t *testing.T) {
	srv := jiraIssueServer(eligiblePayload)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready-for-agent", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	got, err := j.Pick(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ"})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if got != "PROJ-1" {
		t.Errorf("Pick = %q, want PROJ-1 (epic + blocked skipped)", got)
	}
	if runner.calls["pick"] != 0 {
		t.Errorf("expected no MCP fallback, got %d pick calls", runner.calls["pick"])
	}
}

func TestJiraPickFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"pick": {Final: "PICK=PROJ-7"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready-for-agent"}

	got, err := j.Pick(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ"})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if got != "PROJ-7" {
		t.Errorf("Pick = %q, want PROJ-7 from MCP fallback", got)
	}
	if runner.calls["pick"] != 1 {
		t.Errorf("expected one MCP pick, got %d", runner.calls["pick"])
	}
}

// A rest-only tracker (per-repo REST credentials, no MCP runner) must surface a
// rejected token as ErrUnauthorized on the loop's hot path — never fall back to
// the shared Rovo MCP (a different Atlassian identity) — and the nil runner must
// not panic. This is the loop-mode analogue of the onboarding detection guard.
func TestJiraPickRESTOnlySurfacesAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	j := &Jira{Team: "PROJ", ReadyLabel: "ready-for-agent", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "bad"}
	if _, err := j.Pick(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ"}); !errors.Is(err, jiraapi.ErrUnauthorized) {
		t.Fatalf("Pick err = %v, want ErrUnauthorized (no MCP fallback, no panic)", err)
	}
}

// Write operations are just as identity-sensitive: a rest-only SetStatus must
// surface the auth error rather than transition the ticket as the Rovo account.
func TestJiraSetStatusRESTOnlySurfacesAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	j := &Jira{Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "bad"}
	if err := j.SetStatus(context.Background(), "PROJ-7", "In Review", ""); !errors.Is(err, jiraapi.ErrUnauthorized) {
		t.Fatalf("SetStatus err = %v, want ErrUnauthorized (no MCP fallback, no panic)", err)
	}
}

// Epic-scoped Pick resolves entirely over REST when a token is set: it lists the
// epic's leaves (parent query), runs the project eligibility query, and returns
// the highest-ranked candidate that is a leaf — skipping the epic, the blocked
// ticket, and PROJ-5 (eligible but not a leaf of this epic) — without the runner.
func TestJiraPickEpicUsesAPI(t *testing.T) {
	const children = `{"issues":[
		{"key":"PROJ-1","fields":{"summary":"Leaf","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"subtasks":[]}}
	]}`
	const eligible = `{"issues":[
		{"key":"PROJ-2","fields":{"summary":"Epic","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":1},"issuelinks":[]}},
		{"key":"PROJ-5","fields":{"summary":"Not a leaf","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"issuelinks":[]}},
		{"key":"PROJ-1","fields":{"summary":"Leaf","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"issuelinks":[]}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "parent =") {
			_, _ = w.Write([]byte(children))
			return
		}
		_, _ = w.Write([]byte(eligible))
	}))
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready-for-agent", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	got, err := j.Pick(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ", Parent: "PROJ-100"})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if got != "PROJ-1" {
		t.Errorf("Pick = %q, want PROJ-1 (epic + non-leaf skipped)", got)
	}
	if runner.calls["pick"] != 0 {
		t.Errorf("expected no MCP fallback, got %d pick calls", runner.calls["pick"])
	}
}

func TestJiraListEligibleUsesAPIAndKeepsEpics(t *testing.T) {
	srv := jiraIssueServer(eligiblePayload)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready-for-agent", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	list, err := j.ListEligible(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ"})
	if err != nil {
		t.Fatalf("ListEligible error: %v", err)
	}
	// The blocked PROJ-3 is filtered; the epic PROJ-2 is kept (unlike Pick).
	if len(list) != 2 || list[0].ID != "PROJ-2" || list[1].ID != "PROJ-1" {
		t.Errorf("ListEligible = %+v, want [PROJ-2, PROJ-1]", list)
	}
	if runner.calls["list_eligible"] != 0 {
		t.Errorf("expected no MCP fallback, got %d list calls", runner.calls["list_eligible"])
	}
}

func TestJiraListEligibleFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"list_eligible": {Final: `ELIGIBLE=[{"id":"PROJ-1","title":"A"}]`},
	}}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready-for-agent"}

	list, err := j.ListEligible(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ"})
	if err != nil {
		t.Fatalf("ListEligible error: %v", err)
	}
	if len(list) != 1 || list[0].ID != "PROJ-1" {
		t.Errorf("ListEligible = %+v, want [PROJ-1] from MCP fallback", list)
	}
	if runner.calls["list_eligible"] != 1 {
		t.Errorf("expected one MCP list, got %d", runner.calls["list_eligible"])
	}
}

// hierarchyPayload carries a child under an epic, a top-level leaf, and the epic
// itself — so the eligible listing's parent/has_children threading can be asserted.
const hierarchyPayload = `{"issues":[
	{"key":"PROJ-6","fields":{"summary":"Child","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"parent":{"key":"PROJ-5"},"issuelinks":[]}},
	{"key":"PROJ-1","fields":{"summary":"Top","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"issuelinks":[]}},
	{"key":"PROJ-5","fields":{"summary":"Epic","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":1},"issuelinks":[]}}
]}`

func TestJiraListEligibleThreadsHierarchy(t *testing.T) {
	srv := jiraIssueServer(hierarchyPayload)
	defer srv.Close()
	j := &Jira{Runner: &recordingRunner{}, Team: "PROJ", ReadyLabel: "ready-for-agent", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	list, err := j.ListEligible(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ"})
	if err != nil {
		t.Fatalf("ListEligible error: %v", err)
	}
	byID := make(map[string]ListedTicket, len(list))
	for _, tk := range list {
		byID[tk.ID] = tk
	}

	if sub := byID["PROJ-6"]; sub.Parent != "PROJ-5" || sub.HasChildren {
		t.Errorf("sub-issue = %+v, want Parent PROJ-5 and HasChildren false", sub)
	}
	if top := byID["PROJ-1"]; top.Parent != "" || top.HasChildren {
		t.Errorf("top-level = %+v, want empty Parent and HasChildren false", top)
	}
	if epic := byID["PROJ-5"]; !epic.HasChildren || epic.Parent != "" {
		t.Errorf("epic = %+v, want HasChildren true and empty Parent", epic)
	}
}

func TestJiraSubIssuesUsesAPI(t *testing.T) {
	const payload = `{"issues":[
		{"key":"PROJ-10","fields":{"summary":"Leaf","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"subtasks":[]}},
		{"key":"PROJ-11","fields":{"summary":"Parent","status":{"statusCategory":{"key":"done"}},"issuetype":{"hierarchyLevel":0},"subtasks":[{"key":"PROJ-12"}]}}
	]}`
	srv := jiraIssueServer(payload)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	subs, err := j.SubIssues(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("SubIssues error: %v", err)
	}
	want := []SubIssue{
		{ID: "PROJ-10", Title: "Leaf", Done: false, HasChildren: false},
		{ID: "PROJ-11", Title: "Parent", Done: true, HasChildren: true},
	}
	if len(subs) != len(want) {
		t.Fatalf("got %d sub-issues, want %d (%+v)", len(subs), len(want), subs)
	}
	for i := range want {
		if subs[i] != want[i] {
			t.Errorf("sub[%d] = %+v, want %+v", i, subs[i], want[i])
		}
	}
	if runner.calls["sub_issues"] != 0 {
		t.Errorf("expected no MCP fallback, got %d sub_issues calls", runner.calls["sub_issues"])
	}
}

func TestJiraSubIssuesFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"sub_issues": {Final: `SUB_ISSUES=[{"id":"PROJ-2","title":"Child","hasChildren":false}]`},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	subs, err := j.SubIssues(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("SubIssues error: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "PROJ-2" {
		t.Errorf("SubIssues = %+v, want [PROJ-2] from MCP fallback", subs)
	}
	if runner.calls["sub_issues"] != 1 {
		t.Errorf("expected one MCP sub_issues, got %d", runner.calls["sub_issues"])
	}
}

// With a token set, SetStatus drives the two-step REST transition (GET then a
// 204 POST) and never touches the runner.
func TestJiraSetStatusUsesAPI(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"transitions":[{"id":"31","name":"Review","to":{"name":"In Review"}}]}`))
			return
		}
		posts++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	if err := j.SetStatus(context.Background(), "PROJ-7", "In Review", ""); err != nil {
		t.Fatalf("SetStatus error: %v", err)
	}
	if posts != 1 {
		t.Errorf("expected one transition POST, got %d", posts)
	}
	if runner.calls["status"] != 0 {
		t.Errorf("expected no MCP fallback, got %d status calls", runner.calls["status"])
	}
}

// Without a token the direct path is disabled, so SetStatus falls back to the MCP.
func TestJiraSetStatusFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"status": {Final: "DONE"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	if err := j.SetStatus(context.Background(), "PROJ-7", "In Review", ""); err != nil {
		t.Fatalf("SetStatus error: %v", err)
	}
	if runner.calls["status"] != 1 {
		t.Errorf("expected one MCP fallback, got %d status calls", runner.calls["status"])
	}
}

// A target status the workflow has no transition to is a real error, surfaced
// rather than sent to the MCP (which could not resolve a missing status either).
func TestJiraSetStatusSurfacesUnknownStatus(t *testing.T) {
	srv := jiraIssueServer(`{"transitions":[{"id":"11","name":"Start","to":{"name":"In Progress"}}]}`)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	if err := j.SetStatus(context.Background(), "PROJ-7", "Nonexistent", ""); err == nil {
		t.Fatal("SetStatus with an unknown status should error, got nil")
	}
	if runner.calls["status"] != 0 {
		t.Errorf("unknown status must not fall back to MCP, got %d status calls", runner.calls["status"])
	}
}

// With a token set, AddLabel adds the label via a single PUT and never touches
// the runner.
func TestJiraAddLabelUsesAPI(t *testing.T) {
	var puts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts++
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	if err := j.AddLabel(context.Background(), "PROJ-7", "split"); err != nil {
		t.Fatalf("AddLabel error: %v", err)
	}
	if puts != 1 {
		t.Errorf("expected one label PUT, got %d", puts)
	}
	if runner.calls["label"] != 0 {
		t.Errorf("expected no MCP fallback, got %d label calls", runner.calls["label"])
	}
}

// A blank label is a no-op that never calls the API or the runner.
func TestJiraAddLabelBlankIsNoOp(t *testing.T) {
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: "https://x.atlassian.net", Email: "me@acme.com", APIToken: "tok"}
	if err := j.AddLabel(context.Background(), "PROJ-7", "   "); err != nil {
		t.Fatalf("AddLabel error: %v", err)
	}
	if runner.calls["label"] != 0 {
		t.Errorf("blank label must not call the runner, got %d", runner.calls["label"])
	}
}

// Without a token AddLabel falls back to the MCP.
func TestJiraAddLabelFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{"label": {Final: "DONE"}}}
	j := &Jira{Runner: runner, Team: "PROJ"}
	if err := j.AddLabel(context.Background(), "PROJ-7", "split"); err != nil {
		t.Fatalf("AddLabel error: %v", err)
	}
	if runner.calls["label"] != 1 {
		t.Errorf("expected one MCP fallback, got %d label calls", runner.calls["label"])
	}
}

// Reset drops the quarantine label, ensures the ready label (one PUT) and
// transitions back to To Do (GET+POST), all via the API.
func TestJiraResetUsesAPI(t *testing.T) {
	var puts, posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			puts++
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"transitions":[{"id":"11","name":"Backlog","to":{"name":"To Do"}}]}`))
		case http.MethodPost:
			posts++
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready", QuarantineLabel: "quarantine", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	if err := j.Reset(context.Background(), "PROJ-7"); err != nil {
		t.Fatalf("Reset error: %v", err)
	}
	if puts != 1 {
		t.Errorf("expected one label PUT, got %d", puts)
	}
	if posts != 1 {
		t.Errorf("expected one transition POST, got %d", posts)
	}
	if runner.calls["status"] != 0 {
		t.Errorf("expected no MCP fallback, got %d status calls", runner.calls["status"])
	}
}

// Without a token Reset falls back to the MCP transition prompt.
func TestJiraResetFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{"status": {Final: "DONE"}}}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready", QuarantineLabel: "quarantine"}
	if err := j.Reset(context.Background(), "PROJ-7"); err != nil {
		t.Fatalf("Reset error: %v", err)
	}
	if runner.calls["status"] != 1 {
		t.Errorf("expected one MCP fallback, got %d status calls", runner.calls["status"])
	}
}

// Quarantine adds the quarantine label / drops ready (one PUT) and posts a
// reason comment (one POST), all via the API.
func TestJiraQuarantineUsesAPI(t *testing.T) {
	var puts, comments int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			puts++
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			comments++
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready", QuarantineLabel: "quarantine", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	if err := j.Quarantine(context.Background(), "PROJ-7", "boom"); err != nil {
		t.Fatalf("Quarantine error: %v", err)
	}
	if puts != 1 {
		t.Errorf("expected one label PUT, got %d", puts)
	}
	if comments != 1 {
		t.Errorf("expected one comment POST, got %d", comments)
	}
	if runner.calls["quarantine"] != 0 {
		t.Errorf("expected no MCP fallback, got %d quarantine calls", runner.calls["quarantine"])
	}
}

// Without a token Quarantine falls back to the MCP.
func TestJiraQuarantineFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{"quarantine": {Final: "DONE"}}}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready", QuarantineLabel: "quarantine"}
	if err := j.Quarantine(context.Background(), "PROJ-7", "boom"); err != nil {
		t.Fatalf("Quarantine error: %v", err)
	}
	if runner.calls["quarantine"] != 1 {
		t.Errorf("expected one MCP fallback, got %d quarantine calls", runner.calls["quarantine"])
	}
}

// FileBug reads the verdict, resolves the Bug type via createmeta, creates the
// issue and returns its key — no MCP round-trip.
func TestJiraFileBugUsesAPI(t *testing.T) {
	dir := t.TempDir()
	verdict := filepath.Join(dir, "verify.json")
	if err := os.WriteFile(verdict, []byte(`{"pass":false,"summary":"login broken","failures":["500 on submit"]}`), 0o644); err != nil {
		t.Fatalf("write verdict: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"issueTypes":[{"id":"10004","name":"Bug","subtask":false,"hierarchyLevel":0}]}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"10500","key":"PROJ-500"}`))
	}))
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	got, err := j.FileBug(context.Background(), "PROJ-7", verdict)
	if err != nil {
		t.Fatalf("FileBug error: %v", err)
	}
	if got != "PROJ-500" {
		t.Errorf("FileBug = %q, want PROJ-500", got)
	}
	if runner.calls["file_bug"] != 0 {
		t.Errorf("expected no MCP fallback, got %d file_bug calls", runner.calls["file_bug"])
	}
}

// Without a token FileBug falls back to the MCP and parses its BUG= sentinel.
func TestJiraFileBugFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{"file_bug": {Final: "BUG=PROJ-900"}}}
	j := &Jira{Runner: runner, Team: "PROJ"}
	got, err := j.FileBug(context.Background(), "PROJ-7", "/nonexistent/verify.json")
	if err != nil {
		t.Fatalf("FileBug error: %v", err)
	}
	if got != "PROJ-900" {
		t.Errorf("FileBug = %q, want PROJ-900", got)
	}
	if runner.calls["file_bug"] != 1 {
		t.Errorf("expected one MCP fallback, got %d file_bug calls", runner.calls["file_bug"])
	}
}

// bugContent embeds the verdict summary and each failure, and keeps a working
// fallback when the verdict file is missing.
func TestBugContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify.json")
	_ = os.WriteFile(path, []byte(`{"summary":"login broken","failures":["500 on submit","no retry"]}`), 0o644)

	summary, desc := bugContent("PROJ-7", path)
	if summary != "Trau QA blocked PROJ-7: login broken" {
		t.Errorf("summary = %q", summary)
	}
	for _, want := range []string{"login broken", "500 on submit", "no retry", "PROJ-7's run in the trau web UI"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q:\n%s", want, desc)
		}
	}

	_, missing := bugContent("PROJ-7", filepath.Join(dir, "gone.json"))
	if !strings.Contains(missing, "PROJ-7's run in the trau web UI") {
		t.Errorf("missing-verdict description should still point at the run: %q", missing)
	}
}

// EnsureLabels is a no-op on Jira: no API call, no MCP prompt, no error.
func TestJiraEnsureLabelsNoOp(t *testing.T) {
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", ReadyLabel: "ready", QuarantineLabel: "quarantine"}
	if err := j.EnsureLabels(context.Background()); err != nil {
		t.Fatalf("EnsureLabels error: %v", err)
	}
	if runner.calls["ensure_labels"] != 0 {
		t.Errorf("EnsureLabels must not call the runner, got %d", runner.calls["ensure_labels"])
	}
}
