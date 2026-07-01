package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		{"in progress is open", "indeterminate", "", StatusOpen},
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
// not-enabled error and the size judge skips the ticket rather than sizing it.
func TestJiraIssueDetailNoTokenErrors(t *testing.T) {
	j := &Jira{Runner: &recordingRunner{}, Team: "PROJ"}
	if _, err := j.IssueDetail(context.Background(), "PROJ-7"); err == nil {
		t.Fatal("IssueDetail without a token should error, got nil")
	}
}
