package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
	if err := j.SetStatus(context.Background(), "PROJ-7", StageInReview, ""); !errors.Is(err, jiraapi.ErrUnauthorized) {
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

// An epic with two ready children carrying a single "PROJ-1 blocks PROJ-2" link —
// served from both ends, as Jira does — picks the blocker first even though the
// blocked child outranks it in the JQL order.
func TestJiraPickEpicDrainsBlockerBeforeBlockedChild(t *testing.T) {
	const children = `{"issues":[
		{"key":"PROJ-1","fields":{"summary":"A","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"subtasks":[]}},
		{"key":"PROJ-2","fields":{"summary":"B","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"subtasks":[]}}
	]}`
	const eligible = `{"issues":[
		{"key":"PROJ-2","fields":{"summary":"B","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},
			"issuelinks":[{"type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":{"key":"PROJ-1","fields":{"status":{"statusCategory":{"key":"new"}}}}}]}},
		{"key":"PROJ-1","fields":{"summary":"A","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},
			"issuelinks":[{"type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"outwardIssue":{"key":"PROJ-2","fields":{"status":{"statusCategory":{"key":"new"}}}}}]}}
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

	j := &Jira{Team: "PROJ", ReadyLabel: "ready-for-agent", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}
	got, err := j.Pick(context.Background(), Scope{Team: "PROJ", Prefix: "PROJ", Parent: "PROJ-100"})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if got != "PROJ-1" {
		t.Errorf("Pick = %q, want the blocker PROJ-1 before its blocked sibling", got)
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

	if err := j.SetStatus(context.Background(), "PROJ-7", StageInReview, ""); err != nil {
		t.Fatalf("SetStatus error: %v", err)
	}
	if posts != 1 {
		t.Errorf("expected one transition POST, got %d", posts)
	}
	if runner.calls["status"] != 0 {
		t.Errorf("expected no MCP fallback, got %d status calls", runner.calls["status"])
	}
}

// A workflow with no "In Review" status anywhere: the review stage must land on
// the project's own review status rather than failing the transition.
func TestJiraSetStatusResolvesReviewOnAWorkflowWithoutIt(t *testing.T) {
	const workflow = `{"transitions":[
		{"id":"11","name":"QA","to":{"name":"READY FOR QA","statusCategory":{"key":"indeterminate"}}},
		{"id":"21","name":"Back","to":{"name":"To Do","statusCategory":{"key":"new"}}},
		{"id":"31","name":"Start","to":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}},
		{"id":"41","name":"Finish","to":{"name":"Done","statusCategory":{"key":"done"}}}
	]}`
	cases := []struct {
		name     string
		override string
		want     string
	}{
		{"resolved from the workflow", "", "11"},
		{"pinned by STATUS_IN_REVIEW", "In Progress", "31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var post transitionPost
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(workflow))
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&post)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			runner := &recordingRunner{}
			j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}
			if tc.override != "" {
				j.StatusOverrides = map[Stage]string{StageInReview: tc.override}
			}

			if err := j.SetStatus(context.Background(), "PROJ-7", StageInReview, "PR is up."); err != nil {
				t.Fatalf("SetStatus error: %v", err)
			}
			if post.Transition.ID != tc.want {
				t.Errorf("transition id = %q, want %q", post.Transition.ID, tc.want)
			}
			if runner.calls["status"] != 0 {
				t.Errorf("expected no MCP fallback, got %d status calls", runner.calls["status"])
			}
		})
	}
}

// transitionPost is the slice of the transition body these tests assert on.
type transitionPost struct {
	Transition struct {
		ID string `json:"id"`
	} `json:"transition"`
}

// Without a token the direct path is disabled, so SetStatus falls back to the MCP.
func TestJiraSetStatusFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"status": {Final: "DONE"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	if err := j.SetStatus(context.Background(), "PROJ-7", StageInReview, ""); err != nil {
		t.Fatalf("SetStatus error: %v", err)
	}
	if runner.calls["status"] != 1 {
		t.Errorf("expected one MCP fallback, got %d status calls", runner.calls["status"])
	}
}

// A workflow that offers no destination for the stage — no matching name and
// nothing in its category — is a real error, surfaced rather than sent to the MCP
// (which could not transition anywhere either).
func TestJiraSetStatusSurfacesUnreachableStage(t *testing.T) {
	srv := jiraIssueServer(`{"transitions":[{"id":"11","name":"Start","to":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}}]}`)
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	err := j.SetStatus(context.Background(), "PROJ-7", StageDone, "")
	if !errors.Is(err, jiraapi.ErrNoTransition) {
		t.Fatalf("SetStatus err = %v, want ErrNoTransition", err)
	}
	if !strings.Contains(err.Error(), "STATUS_DONE") {
		t.Errorf("error should name the override key, got %q", err.Error())
	}
	if runner.calls["status"] != 0 {
		t.Errorf("an unreachable stage must not fall back to MCP, got %d status calls", runner.calls["status"])
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

// With a token set, RemoveLabel drops the label via a single PUT and never
// touches the runner.
func TestJiraRemoveLabelUsesAPI(t *testing.T) {
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

	if err := j.RemoveLabel(context.Background(), "PROJ-7", "queued"); err != nil {
		t.Fatalf("RemoveLabel error: %v", err)
	}
	if puts != 1 {
		t.Errorf("expected one label PUT, got %d", puts)
	}
	if runner.calls["label"] != 0 {
		t.Errorf("expected no MCP fallback, got %d label calls", runner.calls["label"])
	}
}

// Without a token RemoveLabel falls back to the MCP.
func TestJiraRemoveLabelFallsBackWithoutToken(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{"label": {Final: "DONE"}}}
	j := &Jira{Runner: runner, Team: "PROJ"}
	if err := j.RemoveLabel(context.Background(), "PROJ-7", "queued"); err != nil {
		t.Fatalf("RemoveLabel error: %v", err)
	}
	if runner.calls["label"] != 1 {
		t.Errorf("expected one MCP fallback, got %d label calls", runner.calls["label"])
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

// A Jira-tracked run's QA report lands as one comment on the issue through the
// v3 comment endpoint, with the Markdown converted to real ADF nodes rather than
// a paragraph still carrying its syntax.
func TestJiraPostQANoteCommentsThroughTheAPI(t *testing.T) {
	var (
		method string
		path   string
		sent   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		sent, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	runner := &recordingRunner{}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	err := j.PostQANote(context.Background(), "PROJ-7", QANote{
		Body: "## Trau QA report\n\nVerify passed: all green\n\n- covered the login flow\n\nPR: https://github.test/pr/7\n",
	})
	if err != nil {
		t.Fatalf("PostQANote error: %v", err)
	}
	if method != http.MethodPost || path != "/rest/api/3/issue/PROJ-7/comment" {
		t.Errorf("request = %s %s, want POST /rest/api/3/issue/PROJ-7/comment", method, path)
	}
	var doc struct {
		Body struct {
			Type    string `json:"type"`
			Content []struct {
				Type    string `json:"type"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		} `json:"body"`
	}
	if err := json.Unmarshal(sent, &doc); err != nil {
		t.Fatalf("comment body is not JSON: %v", err)
	}
	if doc.Body.Type != "doc" {
		t.Fatalf("comment body = %s, want an ADF document", sent)
	}
	kinds := make([]string, 0, len(doc.Body.Content))
	for _, block := range doc.Body.Content {
		kinds = append(kinds, block.Type)
	}
	if got := strings.Join(kinds, ","); got != "heading,paragraph,bulletList,paragraph" {
		t.Errorf("ADF blocks = %s, want the heading and list converted to nodes", got)
	}
	if got := doc.Body.Content[0].Content[0].Text; got != "Trau QA report" {
		t.Errorf("heading text = %q, want the report heading without its markdown", got)
	}
	if runner.calls["qa_note"] != 0 {
		t.Errorf("expected no MCP fallback, got %d qa_note calls", runner.calls["qa_note"])
	}
}

// Without API credentials the note goes through the Rovo MCP instead, in a prompt
// carrying the issue and the report body. A prompt cannot carry image bytes, so
// the screenshots are dropped rather than half-described.
func TestJiraPostQANoteFallsBackToMCP(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{"qa_note": {}}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	note := QANote{Body: "## Trau QA report\n\nVerify failed: red tests\n", Images: jiraQAImages}
	if err := j.PostQANote(context.Background(), "PROJ-7", note); err != nil {
		t.Fatalf("PostQANote error: %v", err)
	}
	if runner.calls["qa_note"] != 1 {
		t.Fatalf("qa_note runs = %d, want 1", runner.calls["qa_note"])
	}
	prompt := runner.prompts["qa_note"]
	if !strings.Contains(prompt, "PROJ-7") || !strings.Contains(prompt, "Verify failed: red tests") {
		t.Errorf("MCP prompt = %q, want the issue and the report body", prompt)
	}
	if strings.Contains(prompt, "proof-1.png") {
		t.Errorf("MCP prompt mentions a screenshot it cannot carry:\n%s", prompt)
	}
}

var jiraQAImages = []QAImage{
	{Name: "proof-1.png", Mime: "image/png", Caption: "home", Bytes: []byte("png-1")},
	{Name: "proof-2.png", Mime: "image/png", Caption: "settings", Bytes: []byte("png-2")},
}

// jiraQASite fakes the endpoints a QA note with screenshots touches: the
// multipart attachment upload, the attachment content route whose redirect
// carries the media id — withheld for a filename in noMedia — and the comment
// endpoint. It records the uploads it received and the comment bodies it was
// asked to post.
type jiraQASite struct {
	uploads  []string
	comments [][]byte
}

func newJiraQASite(t *testing.T, noMedia map[string]bool) (*Jira, *jiraQASite) {
	t.Helper()
	site := &jiraQASite{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/attachments"):
			_, head, err := r.FormFile("file")
			if err != nil {
				t.Errorf("FormFile: %v", err)
				return
			}
			site.uploads = append(site.uploads, head.Filename)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `[{"id":%q}]`, "att-"+head.Filename)
		case strings.Contains(path, "/attachment/content/"):
			name := strings.TrimPrefix(path[strings.LastIndex(path, "/")+1:], "att-")
			if noMedia[name] {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Location", "https://api.media.atlassian.com/file/media-"+name+"/binary?token=abc")
			w.WriteHeader(http.StatusSeeOther)
		case strings.HasSuffix(path, "/comment"):
			body, _ := io.ReadAll(r.Body)
			site.comments = append(site.comments, body)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unrouted request %s %s", r.Method, path)
		}
	}))
	t.Cleanup(srv.Close)
	return &Jira{Runner: &recordingRunner{}, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}, site
}

// qaCommentDoc is the shape a QA comment's ADF is read back in.
type qaCommentDoc struct {
	Body struct {
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Type  string            `json:"type"`
				Text  string            `json:"text"`
				Attrs map[string]string `json:"attrs"`
			} `json:"content"`
		} `json:"content"`
	} `json:"body"`
}

func (s *jiraQASite) comment(t *testing.T) qaCommentDoc {
	t.Helper()
	if len(s.comments) != 1 {
		t.Fatalf("posted %d comments, want exactly 1", len(s.comments))
	}
	var doc qaCommentDoc
	if err := json.Unmarshal(s.comments[0], &doc); err != nil {
		t.Fatalf("comment body is not JSON: %v", err)
	}
	return doc
}

func (d qaCommentDoc) kinds() string {
	out := make([]string, 0, len(d.Body.Content))
	for _, block := range d.Body.Content {
		out = append(out, block.Type)
	}
	return strings.Join(out, ",")
}

// Every screenshot is uploaded to the issue as an attachment and embedded in the
// one comment the run posts, by the media id its content redirect names.
func TestJiraPostQANoteAttachesScreenshotsAndEmbedsThem(t *testing.T) {
	j, site := newJiraQASite(t, nil)

	note := QANote{Body: "## Trau QA report\n\nVerify passed\n", Images: jiraQAImages}
	if err := j.PostQANote(context.Background(), "PROJ-7", note); err != nil {
		t.Fatalf("PostQANote error: %v", err)
	}

	if !slices.Equal(site.uploads, []string{"proof-1.png", "proof-2.png"}) {
		t.Errorf("uploads = %v, want both screenshots attached", site.uploads)
	}
	doc := site.comment(t)
	if got := doc.kinds(); got != "heading,paragraph,mediaSingle,mediaSingle" {
		t.Fatalf("ADF blocks = %s, want the report followed by one media node per screenshot", got)
	}
	for i, want := range []string{"media-proof-1.png", "media-proof-2.png"} {
		media := doc.Body.Content[2+i].Content
		if len(media) != 1 || media[0].Attrs["type"] != "file" || media[0].Attrs["id"] != want {
			t.Errorf("media node %d = %+v, want a file node for %s", i, media, want)
		}
	}
	if strings.Contains(string(site.comments[0]), qaAttachmentsNote) {
		t.Errorf("comment points at the attachments although both embedded:\n%s", site.comments[0])
	}
}

// The media id is a documented workaround, not a supported read: a screenshot it
// cannot be resolved for is still attached to the issue, and the comment says so
// instead of carrying a media node that would render as a broken image.
func TestJiraPostQANoteFallsBackToAttachmentsWhenTheMediaIDIsUnreadable(t *testing.T) {
	j, site := newJiraQASite(t, map[string]bool{"proof-2.png": true})

	note := QANote{Body: "## Trau QA report\n\nVerify passed\n", Images: jiraQAImages}
	if err := j.PostQANote(context.Background(), "PROJ-7", note); err != nil {
		t.Fatalf("PostQANote error: %v", err)
	}

	if !slices.Equal(site.uploads, []string{"proof-1.png", "proof-2.png"}) {
		t.Errorf("uploads = %v, want both screenshots attached even so", site.uploads)
	}
	doc := site.comment(t)
	if got := doc.kinds(); got != "heading,paragraph,paragraph,mediaSingle" {
		t.Fatalf("ADF blocks = %s, want only the resolved screenshot embedded", got)
	}
	if got := doc.Body.Content[3].Content[0].Attrs["id"]; got != "media-proof-1.png" {
		t.Errorf("embedded media id = %q, want the screenshot that resolved", got)
	}
	if got := doc.Body.Content[2].Content[0].Text; got != qaAttachmentsNote {
		t.Errorf("comment tail = %q, want %q", got, qaAttachmentsNote)
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
