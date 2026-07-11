package linearapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectIssuesFiltersByProjectAndMapsComments(t *testing.T) {
	const payload = `{"data":{"issues":{
		"pageInfo":{"hasNextPage":false,"endCursor":""},
		"nodes":[
			{"id":"iss-1","identifier":"COD-1","title":"First","description":"Body",
			 "priority":2,"dueDate":"2026-08-01","url":"https://linear.app/acme/issue/COD-1",
			 "createdAt":"2026-07-01T00:00:00Z","updatedAt":"2026-07-10T12:00:00Z",
			 "state":{"name":"In Progress","type":"started"},
			 "project":{"id":"proj-1","name":"trau"},
			 "parent":{"identifier":"COD-9"},
			 "labels":{"nodes":[{"name":"ready-for-agent"}]},
			 "children":{"nodes":[]},
			 "comments":{"nodes":[
				{"id":"c1","body":"looks good","createdAt":"2026-07-02T00:00:00Z","updatedAt":"2026-07-02T00:00:00Z","user":{"name":"Ada"}},
				{"id":"c2","body":"bot note","createdAt":"2026-07-03T00:00:00Z","updatedAt":"2026-07-03T00:00:00Z","user":null}
			 ]}}
		]}}}`

	var req graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	c := New("lin_key")
	c.Endpoint = srv.URL
	issues, err := c.ProjectIssues(context.Background(), "team-1", "proj-1", "")
	if err != nil {
		t.Fatalf("ProjectIssues: %v", err)
	}

	if !strings.Contains(req.Query, "SyncIssues") {
		t.Fatalf("query is not the sync query: %s", req.Query)
	}
	filter, ok := req.Variables["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter variable = %#v, want a project filter", req.Variables["filter"])
	}
	if _, hasProject := filter["project"]; !hasProject {
		t.Fatalf("filter = %#v, want a server-side project filter", filter)
	}

	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	iss := issues[0]
	if iss.Identifier != "COD-1" || iss.Description != "Body" || iss.URL == "" {
		t.Fatalf("issue = %+v, want COD-1 with description and url", iss)
	}
	if iss.Parent != "COD-9" || iss.UpdatedAt != "2026-07-10T12:00:00Z" {
		t.Fatalf("issue parent/updatedAt = %q/%q", iss.Parent, iss.UpdatedAt)
	}
	if len(iss.Comments) != 2 || iss.Comments[0].Author != "Ada" || iss.Comments[1].Author != "" {
		t.Fatalf("comments = %+v, want two with Ada then empty author", iss.Comments)
	}
}

func TestProjectIssuesFallsBackToTeamFilter(t *testing.T) {
	var req graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`)
	}))
	defer srv.Close()

	c := New("lin_key")
	c.Endpoint = srv.URL
	if _, err := c.ProjectIssues(context.Background(), "team-1", "", ""); err != nil {
		t.Fatalf("ProjectIssues: %v", err)
	}
	filter, _ := req.Variables["filter"].(map[string]any)
	if _, hasTeam := filter["team"]; !hasTeam {
		t.Fatalf("filter = %#v, want a team filter when no project id", filter)
	}
}

func TestProjectIssuesNeedsATarget(t *testing.T) {
	c := New("lin_key")
	if _, err := c.ProjectIssues(context.Background(), "", "", ""); err != ErrNotEnabled {
		t.Fatalf("ProjectIssues with no target = %v, want ErrNotEnabled", err)
	}
}

func TestProjectIssuesIncrementalFiltersUpdatedSince(t *testing.T) {
	var req graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`)
	}))
	defer srv.Close()

	c := New("lin_key")
	c.Endpoint = srv.URL
	if _, err := c.ProjectIssues(context.Background(), "team-1", "proj-1", "2026-07-10T12:00:00Z"); err != nil {
		t.Fatalf("ProjectIssues: %v", err)
	}
	filter, _ := req.Variables["filter"].(map[string]any)
	updated, ok := filter["updatedAt"].(map[string]any)
	if !ok {
		t.Fatalf("filter = %#v, want an updatedAt clause for an incremental pull", filter)
	}
	if updated["gt"] != "2026-07-10T12:00:00Z" {
		t.Fatalf("updatedAt = %#v, want gt the cursor", updated)
	}
}

func TestProjectIssuesUnparseableCursorFallsBackToFullPull(t *testing.T) {
	var req graphReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"issues":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}`)
	}))
	defer srv.Close()

	c := New("lin_key")
	c.Endpoint = srv.URL
	if _, err := c.ProjectIssues(context.Background(), "team-1", "proj-1", "not-a-timestamp"); err != nil {
		t.Fatalf("ProjectIssues: %v", err)
	}
	filter, _ := req.Variables["filter"].(map[string]any)
	if _, hasProject := filter["project"]; !hasProject {
		t.Fatalf("filter = %#v, want the full project filter", filter)
	}
	if _, hasUpdated := filter["updatedAt"]; hasUpdated {
		t.Fatalf("filter = %#v, want no updatedAt clause for an unparseable cursor", filter)
	}
}
