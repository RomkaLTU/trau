package jiraapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSyncIssuesFiltersByProjectAndMapsContent(t *testing.T) {
	const payload = `{"issues":[
		{"key":"PROJ-1","fields":{
			"summary":"An epic",
			"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Epic body"}]}]},
			"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
			"priority":{"name":"High"},
			"duedate":"2026-08-01",
			"issuetype":{"hierarchyLevel":1},
			"labels":["epic"],
			"created":"2026-07-01T00:00:00.000+0000",
			"updated":"2026-07-10T00:00:00.000+0000",
			"comment":{"comments":[
				{"id":"10","author":{"displayName":"Ada"},"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"shipping soon"}]}]},"created":"2026-07-02T00:00:00.000+0000","updated":"2026-07-02T00:00:00.000+0000"}
			]}
		}}
	]}`

	var gotReq searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	issues, err := New(srv.URL, "me@acme.com", "tok").SyncIssues(context.Background(), "PROJ")
	if err != nil {
		t.Fatalf("SyncIssues: %v", err)
	}

	if !strings.Contains(gotReq.JQL, `project = "PROJ"`) || !strings.Contains(gotReq.JQL, "ORDER BY updated DESC") {
		t.Fatalf("JQL = %q, want project filter ordered by updated", gotReq.JQL)
	}
	if !containsField(gotReq.Fields, "comment") || !containsField(gotReq.Fields, "description") {
		t.Fatalf("fields = %v, want comment and description requested", gotReq.Fields)
	}

	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	iss := issues[0]
	if iss.Key != "PROJ-1" || iss.Description != "Epic body" || !iss.IsEpic {
		t.Fatalf("issue = %+v, want PROJ-1 epic with description", iss)
	}
	if iss.Priority != 2 || iss.DueDate != "2026-08-01" {
		t.Fatalf("priority/duedate = %d/%q, want 2/2026-08-01", iss.Priority, iss.DueDate)
	}
	if len(iss.Comments) != 1 || iss.Comments[0].Author != "Ada" || iss.Comments[0].Body != "shipping soon" {
		t.Fatalf("comments = %+v, want one from Ada", iss.Comments)
	}
}

func TestSyncIssuesNeedsProject(t *testing.T) {
	if _, err := New("https://acme.atlassian.net", "me@acme.com", "tok").SyncIssues(context.Background(), "  "); err != ErrNotEnabled {
		t.Fatalf("SyncIssues with blank project = %v, want ErrNotEnabled", err)
	}
}

func TestMapPriority(t *testing.T) {
	cases := map[string]int{"Highest": 1, "high": 2, "Medium": 3, "Low": 4, "Lowest": 5, "": 0, "weird": 0}
	for name, want := range cases {
		if got := mapPriority(name); got != want {
			t.Errorf("mapPriority(%q) = %d, want %d", name, got, want)
		}
	}
}

func containsField(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}
