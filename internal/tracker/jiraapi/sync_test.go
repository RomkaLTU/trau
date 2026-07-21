package jiraapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
			"assignee":{"accountId":"acc-7","displayName":"Ada Lovelace"},
			"labels":["epic"],
			"created":"2026-07-01T00:00:00.000+0000",
			"updated":"2026-07-10T00:00:00.000+0000",
			"comment":{"comments":[
				{"id":"10","author":{"displayName":"Ada"},"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"shipping soon"}]}]},"created":"2026-07-02T00:00:00.000+0000","updated":"2026-07-02T00:00:00.000+0000"}
			]},
			"issuelinks":[
				{"type":{"name":"Blocks","inward":"is blocked by"},"inwardIssue":{"key":"PROJ-9","fields":{"status":{"name":"Done","statusCategory":{"key":"done"}}}}},
				{"type":{"name":"Blocks","inward":"is blocked by"},"inwardIssue":{"key":"PROJ-8","fields":{"status":{"name":"Todo","statusCategory":{"key":"new"}}}}},
				{"type":{"name":"Relates","inward":"relates to"},"inwardIssue":{"key":"PROJ-7","fields":{"status":{"name":"Todo","statusCategory":{"key":"new"}}}}}
			]
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

	issues, err := New(srv.URL, "me@acme.com", "tok").SyncIssues(context.Background(), "PROJ", "")
	if err != nil {
		t.Fatalf("SyncIssues: %v", err)
	}

	if !strings.Contains(gotReq.JQL, `project = "PROJ"`) || !strings.Contains(gotReq.JQL, "ORDER BY updated DESC") {
		t.Fatalf("JQL = %q, want project filter ordered by updated", gotReq.JQL)
	}
	if !containsField(gotReq.Fields, "comment") || !containsField(gotReq.Fields, "description") {
		t.Fatalf("fields = %v, want comment and description requested", gotReq.Fields)
	}
	if !containsField(gotReq.Fields, "assignee") {
		t.Fatalf("fields = %v, want assignee requested", gotReq.Fields)
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
	if iss.AssigneeID != "acc-7" || iss.AssigneeName != "Ada Lovelace" {
		t.Fatalf("assignee = %q/%q, want acc-7/Ada Lovelace", iss.AssigneeID, iss.AssigneeName)
	}
	if len(iss.Comments) != 1 || iss.Comments[0].Author != "Ada" || iss.Comments[0].Body != "shipping soon" {
		t.Fatalf("comments = %+v, want one from Ada", iss.Comments)
	}
	if !containsField(gotReq.Fields, "issuelinks") {
		t.Fatalf("fields = %v, want issuelinks requested", gotReq.Fields)
	}
	want := []Blocker{{Key: "PROJ-9", Resolved: true}, {Key: "PROJ-8"}}
	if !reflect.DeepEqual(iss.BlockedBy, want) {
		t.Fatalf("blockedBy = %+v, want the blocked-by links with resolution, not the relates link", iss.BlockedBy)
	}
}

func TestSyncIssuesNeedsProject(t *testing.T) {
	if _, err := New("https://acme.atlassian.net", "me@acme.com", "tok").SyncIssues(context.Background(), "  ", ""); err != ErrNotEnabled {
		t.Fatalf("SyncIssues with blank project = %v, want ErrNotEnabled", err)
	}
}

func TestSyncIssuesIncrementalNarrowsJQL(t *testing.T) {
	tests := []struct {
		name  string
		since string
		want  string
	}{
		{name: "valid cursor", since: "2026-07-10T09:30:00.000+0000", want: `updated >= "2026-07-10 09:30"`},
		{name: "unparseable cursor falls back to full pull", since: "not-a-timestamp", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq searchRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &gotReq)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"issues":[]}`))
			}))
			defer srv.Close()

			if _, err := New(srv.URL, "me@acme.com", "tok").SyncIssues(context.Background(), "PROJ", tt.since); err != nil {
				t.Fatalf("SyncIssues: %v", err)
			}
			if tt.want == "" {
				if strings.Contains(gotReq.JQL, "updated >=") {
					t.Fatalf("JQL = %q, want no incremental clause for an unparseable cursor", gotReq.JQL)
				}
				return
			}
			if !strings.Contains(gotReq.JQL, tt.want) {
				t.Fatalf("JQL = %q, want it to contain %q", gotReq.JQL, tt.want)
			}
		})
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
