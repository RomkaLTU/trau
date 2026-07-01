package jiraapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestEligibleJQL(t *testing.T) {
	cases := []struct {
		name    string
		project string
		label   string
		want    string
	}{
		{
			"project and label",
			"PROJ", "ready-for-agent",
			`project = "PROJ" AND labels = "ready-for-agent" AND statusCategory = "To Do" AND resolution = EMPTY ORDER BY priority DESC, duedate ASC, key ASC`,
		},
		{
			"blank label drops the labels clause",
			"PROJ", "  ",
			`project = "PROJ" AND statusCategory = "To Do" AND resolution = EMPTY ORDER BY priority DESC, duedate ASC, key ASC`,
		},
	}
	for _, tc := range cases {
		if got := eligibleJQL(tc.project, tc.label); got != tc.want {
			t.Errorf("%s: eligibleJQL = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestJQLQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{`PROJ`, `"PROJ"`},
		{`ready label`, `"ready label"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
	}
	for _, tc := range cases {
		if got := jqlQuote(tc.in); got != tc.want {
			t.Errorf("jqlQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEligibleDisabledWithoutToken(t *testing.T) {
	c := New("", "", "")
	if _, err := c.Eligible(context.Background(), "PROJ", "ready"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Eligible err = %v, want ErrNotEnabled", err)
	}
	if _, err := c.SubIssues(context.Background(), "PROJ-1"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("SubIssues err = %v, want ErrNotEnabled", err)
	}
}

// A token-enabled client that has no project to search yields ErrNotEnabled so the
// tracker falls back to the MCP rather than issuing a project-less query.
func TestEligibleWithoutProjectNotEnabled(t *testing.T) {
	c := New("https://acme.atlassian.net", "me@acme.com", "tok")
	if _, err := c.Eligible(context.Background(), "  ", "ready"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Eligible err = %v, want ErrNotEnabled", err)
	}
}

func TestEligiblePostsJQLAndParses(t *testing.T) {
	const payload = `{"issues":[
		{"key":"PROJ-1","fields":{
			"summary":"First","status":{"name":"To Do","statusCategory":{"key":"new"}},
			"issuetype":{"hierarchyLevel":0},
			"issuelinks":[{"type":{"name":"Blocks","inward":"is blocked by"},"inwardIssue":{"key":"PROJ-9","fields":{"status":{"statusCategory":{"key":"done"}}}}}]
		}},
		{"key":"PROJ-2","fields":{
			"summary":"An epic","status":{"name":"To Do","statusCategory":{"key":"new"}},
			"issuetype":{"hierarchyLevel":1},"issuelinks":[]
		}},
		{"key":"PROJ-3","fields":{
			"summary":"Blocked","status":{"name":"To Do","statusCategory":{"key":"new"}},
			"issuetype":{"hierarchyLevel":0},
			"issuelinks":[{"type":{"name":"Blocks","inward":"is blocked by"},"inwardIssue":{"key":"PROJ-8","fields":{"status":{"statusCategory":{"key":"indeterminate"}}}}}]
		}}
	]}`

	var gotMethod, gotPath string
	var gotReq searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	cands, err := New(srv.URL, "me@acme.com", "tok").Eligible(context.Background(), "PROJ", "ready-for-agent")
	if err != nil {
		t.Fatalf("Eligible error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/rest/api/3/search/jql" {
		t.Errorf("path = %q, want /rest/api/3/search/jql", gotPath)
	}
	if !strings.Contains(gotReq.JQL, `project = "PROJ"`) ||
		!strings.Contains(gotReq.JQL, `labels = "ready-for-agent"`) ||
		!strings.Contains(gotReq.JQL, `statusCategory = "To Do"`) ||
		!strings.Contains(gotReq.JQL, `resolution = EMPTY`) {
		t.Errorf("request JQL missing expected clauses: %q", gotReq.JQL)
	}
	if !reflect.DeepEqual(gotReq.Fields, eligibleFields) {
		t.Errorf("request fields = %v, want %v", gotReq.Fields, eligibleFields)
	}

	if len(cands) != 3 {
		t.Fatalf("got %d candidates, want 3", len(cands))
	}
	if cands[0].Key != "PROJ-1" || cands[0].StatusName != "To Do" || cands[0].IsEpic {
		t.Errorf("candidate[0] = %+v, want PROJ-1 non-epic To Do", cands[0])
	}
	if len(cands[0].BlockedBy) != 1 || cands[0].BlockedBy[0].Key != "PROJ-9" || !cands[0].BlockedBy[0].Resolved {
		t.Errorf("candidate[0] blockers = %+v, want one resolved PROJ-9", cands[0].BlockedBy)
	}
	if !cands[1].IsEpic {
		t.Errorf("candidate[1] should be an epic (hierarchyLevel 1)")
	}
	if len(cands[2].BlockedBy) != 1 || cands[2].BlockedBy[0].Resolved {
		t.Errorf("candidate[2] blocker should be unresolved: %+v", cands[2].BlockedBy)
	}
}

// A "relates to" link is not a blocker and must be ignored, so an issue with only
// non-blocking links reports no blockers.
func TestEligibleConsidersOnlyBlockedByLinks(t *testing.T) {
	const payload = `{"issues":[{"key":"PROJ-1","fields":{
		"summary":"S","status":{"name":"To Do","statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},
		"issuelinks":[{"type":{"name":"Relates","inward":"relates to"},"inwardIssue":{"key":"PROJ-5","fields":{"status":{"statusCategory":{"key":"new"}}}}}]
	}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	cands, err := New(srv.URL, "me@acme.com", "tok").Eligible(context.Background(), "PROJ", "ready")
	if err != nil {
		t.Fatalf("Eligible error: %v", err)
	}
	if len(cands) != 1 || len(cands[0].BlockedBy) != 0 {
		t.Errorf("non-blocking link should yield no blockers, got %+v", cands)
	}
}

func TestEligiblePaginates(t *testing.T) {
	var requests int
	var secondToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req searchRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.NextPageToken == "" {
			_, _ = w.Write([]byte(`{"issues":[{"key":"PROJ-1","fields":{"summary":"one"}}],"nextPageToken":"tok2"}`))
			return
		}
		secondToken = req.NextPageToken
		_, _ = w.Write([]byte(`{"issues":[{"key":"PROJ-2","fields":{"summary":"two"}}]}`))
	}))
	defer srv.Close()

	cands, err := New(srv.URL, "me@acme.com", "tok").Eligible(context.Background(), "PROJ", "ready")
	if err != nil {
		t.Fatalf("Eligible error: %v", err)
	}
	if requests != 2 {
		t.Errorf("made %d requests, want 2 (one per page)", requests)
	}
	if secondToken != "tok2" {
		t.Errorf("second request carried token %q, want tok2", secondToken)
	}
	if len(cands) != 2 || cands[0].Key != "PROJ-1" || cands[1].Key != "PROJ-2" {
		t.Errorf("paged candidates = %+v, want PROJ-1 then PROJ-2", cands)
	}
}

func TestSubIssuesParsesChildren(t *testing.T) {
	const payload = `{"issues":[
		{"key":"PROJ-10","fields":{"summary":"Leaf","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":0},"subtasks":[]}},
		{"key":"PROJ-11","fields":{"summary":"Has subtasks","status":{"statusCategory":{"key":"indeterminate"}},"issuetype":{"hierarchyLevel":0},"subtasks":[{"key":"PROJ-12"}]}},
		{"key":"PROJ-13","fields":{"summary":"Done","status":{"statusCategory":{"key":"done"}},"issuetype":{"hierarchyLevel":0},"subtasks":[]}},
		{"key":"PROJ-14","fields":{"summary":"Nested epic","status":{"statusCategory":{"key":"new"}},"issuetype":{"hierarchyLevel":1},"subtasks":[]}}
	]}`

	var gotReq searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	children, err := New(srv.URL, "me@acme.com", "tok").SubIssues(context.Background(), "PROJ-1")
	if err != nil {
		t.Fatalf("SubIssues error: %v", err)
	}
	if !strings.Contains(gotReq.JQL, `parent = "PROJ-1"`) {
		t.Errorf("SubIssues JQL = %q, want a parent clause", gotReq.JQL)
	}
	if !reflect.DeepEqual(gotReq.Fields, childFields) {
		t.Errorf("request fields = %v, want %v", gotReq.Fields, childFields)
	}
	want := []Child{
		{Key: "PROJ-10", Summary: "Leaf", Done: false, HasChildren: false},
		{Key: "PROJ-11", Summary: "Has subtasks", Done: false, HasChildren: true},
		{Key: "PROJ-13", Summary: "Done", Done: true, HasChildren: false},
		{Key: "PROJ-14", Summary: "Nested epic", Done: false, HasChildren: true},
	}
	if !reflect.DeepEqual(children, want) {
		t.Errorf("children = %+v, want %+v", children, want)
	}
}
