package jiraapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewDisabledWithoutCredentials(t *testing.T) {
	cases := []struct {
		name                  string
		baseURL, email, token string
	}{
		{"all empty", "", "", ""},
		{"empty token", "https://acme.atlassian.net", "me@acme.com", ""},
		{"empty email", "https://acme.atlassian.net", "", "tok"},
		{"empty base url", "", "me@acme.com", "tok"},
	}
	for _, tc := range cases {
		c := New(tc.baseURL, tc.email, tc.token)
		if c.enabled() {
			t.Errorf("%s: client should be disabled", tc.name)
		}
		if _, err := c.Issue(context.Background(), "PROJ-1"); !errors.Is(err, ErrNotEnabled) {
			t.Errorf("%s: Issue err = %v, want ErrNotEnabled", tc.name, err)
		}
	}
}

func TestIssueSendsBasicAuthAndReturnsSummary(t *testing.T) {
	const email, token = "me@acme.com", "s3cr3t"
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))

	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if got := r.URL.Query().Get("fields"); got != "summary,description,status,resolution,project,parent" {
			t.Errorf("fields query = %q, want the widened field set", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-414","fields":{"summary":"Ship the thing"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, email, token)
	issue, err := c.Issue(context.Background(), "PROJ-414")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	if gotPath != "/rest/api/3/issue/PROJ-414" {
		t.Errorf("request path = %q, want /rest/api/3/issue/PROJ-414", gotPath)
	}
	if issue.Summary != "Ship the thing" {
		t.Errorf("summary = %q, want %q", issue.Summary, "Ship the thing")
	}
}

func TestIssueMapsStatusToSentinel(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrUnauthorized},
		{"forbidden", http.StatusForbidden, ErrUnauthorized},
		{"not found", http.StatusNotFound, ErrNotFound},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		c := New(srv.URL, "me@acme.com", "tok")
		_, err := c.Issue(context.Background(), "PROJ-1")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: Issue err = %v, want %v", tc.name, err, tc.want)
		}
		srv.Close()
	}
}

func TestIssueMapsAllFields(t *testing.T) {
	const payload = `{
		"key":"PROJ-9",
		"fields":{
			"summary":"Do the thing",
			"description":{"type":"doc","version":1,"content":[
				{"type":"paragraph","content":[{"type":"text","text":"Line one."}]},
				{"type":"paragraph","content":[{"type":"text","text":"Line two."}]}
			]},
			"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
			"resolution":{"name":"Done"},
			"project":{"key":"PROJ","name":"Project X","id":"10001"},
			"parent":{"key":"PROJ-1"}
		}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	issue, err := New(srv.URL, "me@acme.com", "tok").Issue(context.Background(), "PROJ-9")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}
	if issue.Summary != "Do the thing" {
		t.Errorf("Summary = %q, want %q", issue.Summary, "Do the thing")
	}
	if issue.Description != "Line one.\nLine two." {
		t.Errorf("Description = %q, want %q", issue.Description, "Line one.\nLine two.")
	}
	if issue.Status.Category != "indeterminate" || issue.Status.Name != "In Progress" {
		t.Errorf("Status = %+v, want {In Progress indeterminate}", issue.Status)
	}
	if issue.Resolution != "Done" {
		t.Errorf("Resolution = %q, want %q", issue.Resolution, "Done")
	}
	if issue.Project != (Project{Key: "PROJ", Name: "Project X", ID: "10001"}) {
		t.Errorf("Project = %+v, want {PROJ Project X 10001}", issue.Project)
	}
	if issue.Parent != "PROJ-1" {
		t.Errorf("Parent = %q, want %q", issue.Parent, "PROJ-1")
	}
}

// A minimal issue (no description/status/resolution/project/parent) must map to
// zero values without panicking on the absent optional objects.
func TestIssueHandlesMissingOptionalFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-2","fields":{"summary":"S","description":null}}`))
	}))
	defer srv.Close()

	issue, err := New(srv.URL, "me@acme.com", "tok").Issue(context.Background(), "PROJ-2")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}
	if issue.Description != "" || issue.Resolution != "" || issue.Parent != "" {
		t.Errorf("optional fields not empty: %+v", issue)
	}
	if issue.Status != (Status{}) || issue.Project != (Project{}) {
		t.Errorf("optional objects not zero: status=%+v project=%+v", issue.Status, issue.Project)
	}
}

func TestADFToText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"nil", "", ""},
		{"null", "null", ""},
		{"malformed", "{not json", ""},
		{"single paragraph", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]}]}`, "Hello world"},
		{"two paragraphs", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"P1"}]},{"type":"paragraph","content":[{"type":"text","text":"P2"}]}]}`, "P1\nP2"},
		{"hard break", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Line1"},{"type":"hardBreak"},{"type":"text","text":"Line2"}]}]}`, "Line1\nLine2"},
		{"bullet list", `{"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"A"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"B"}]}]}]}]}`, "A\n\nB"},
		{"heading then paragraph", `{"type":"doc","content":[{"type":"heading","content":[{"type":"text","text":"Title"}]},{"type":"paragraph","content":[{"type":"text","text":"Body"}]}]}`, "Title\nBody"},
		{"marks ignored", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"see "},{"type":"text","text":"bold","marks":[{"type":"strong"}]},{"type":"text","text":" here"}]}]}`, "see bold here"},
	}
	for _, tc := range cases {
		if got := adfToText(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("%s: adfToText = %q, want %q", tc.name, got, tc.want)
		}
	}
}
