package jiraapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAssignIssueSendsAccountID(t *testing.T) {
	var (
		method string
		path   string
		body   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := New(srv.URL, "me@acme.com", "tok").AssignIssue(context.Background(), "PROJ-7", "acc-1"); err != nil {
		t.Fatalf("AssignIssue error: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %q, want PUT", method)
	}
	if path != "/rest/api/3/issue/PROJ-7/assignee" {
		t.Errorf("path = %q, want the v3 assignee endpoint for PROJ-7", path)
	}
	if body != `{"accountId":"acc-1"}` {
		t.Errorf("body = %s, want the accountId", body)
	}
}

// Clearing an assignee must send an explicit null accountId: an omitted field
// leaves the current assignee in place.
func TestAssignIssueClearsWithNullAccountID(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := New(srv.URL, "me@acme.com", "tok").AssignIssue(context.Background(), "PROJ-7", " "); err != nil {
		t.Fatalf("AssignIssue error: %v", err)
	}
	if body != `{"accountId":null}` {
		t.Errorf("body = %s, want an explicit null accountId", body)
	}
}

func TestAssignableUsersSearchesTheProject(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"accountId": "acc-1", "displayName": "Ada"},
			{"accountId": "acc-2", "displayName": "Bob"},
		})
	}))
	defer srv.Close()

	users, err := New(srv.URL, "me@acme.com", "tok").AssignableUsers(context.Background(), "PROJ", " ad ")
	if err != nil {
		t.Fatalf("AssignableUsers error: %v", err)
	}
	want := []User{{ID: "acc-1", Name: "Ada"}, {ID: "acc-2", Name: "Bob"}}
	for i, u := range want {
		if users[i] != u {
			t.Errorf("user[%d] = %+v, want %+v", i, users[i], u)
		}
	}
	got, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse query %q: %v", query, err)
	}
	if got.Get("project") != "PROJ" || got.Get("query") != "ad" || got.Get("maxResults") != "50" {
		t.Errorf("query = %q, want the project, the trimmed query and a bounded page", query)
	}
}

func TestAssignmentDisabledWithoutToken(t *testing.T) {
	if err := New("", "", "").AssignIssue(context.Background(), "PROJ-7", "acc-1"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("AssignIssue err = %v, want ErrNotEnabled", err)
	}
	if _, err := New("", "", "").AssignableUsers(context.Background(), "PROJ", ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("AssignableUsers err = %v, want ErrNotEnabled", err)
	}
	c := New("https://x.atlassian.net", "me@acme.com", "tok")
	if _, err := c.AssignableUsers(context.Background(), " ", ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("AssignableUsers without a project = %v, want ErrNotEnabled", err)
	}
}
