package jiraapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestProjectStatusesDisabledWithoutToken(t *testing.T) {
	if _, err := New("", "", "").ProjectStatuses(context.Background(), "PROJ"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("ProjectStatuses err = %v, want ErrNotEnabled", err)
	}
}

func TestProjectStatusesNeedsAProjectKey(t *testing.T) {
	c := New("https://acme.atlassian.net", "me@acme.com", "tok")
	if _, err := c.ProjectStatuses(context.Background(), "  "); !errors.Is(err, ErrNotFound) {
		t.Errorf("ProjectStatuses err = %v, want ErrNotFound", err)
	}
}

// Jira reports statuses per issue type, and a project's issue types overlap
// heavily: the union is what a status mapping keys on, deduped by name so a
// status three issue types share is offered once.
func TestProjectStatusesUnionsIssueTypesAndDedupes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`[
			{"id":"10001","name":"Story","statuses":[
				{"name":"To Do","statusCategory":{"key":"new"}},
				{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
				{"name":"Done","statusCategory":{"key":"done"}}]},
			{"id":"10002","name":"Bug","statuses":[
				{"name":"To Do","statusCategory":{"key":"new"}},
				{"name":"Ready for QA","statusCategory":{"key":"indeterminate"}},
				{"name":"Done","statusCategory":{"key":"done"}}]},
			{"id":"10003","name":"Epic","statuses":[
				{"name":"  to do  ","statusCategory":{"key":"new"}},
				{"name":"Icebox","statusCategory":{"key":"undefined"}}]}]`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "me@acme.com", "tok").ProjectStatuses(context.Background(), "PROJ")
	if err != nil {
		t.Fatalf("ProjectStatuses error: %v", err)
	}
	if want := "/rest/api/3/project/PROJ/statuses"; gotPath != want {
		t.Errorf("requested %q, want %q", gotPath, want)
	}
	want := []Status{
		{Name: "To Do", Category: "new"},
		{Name: "In Progress", Category: "indeterminate"},
		{Name: "Done", Category: "done"},
		{Name: "Ready for QA", Category: "indeterminate"},
		{Name: "Icebox", Category: "undefined"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProjectStatuses = %+v, want %+v", got, want)
	}
}

// A project key with a space or a slash is a legal Jira key holder for the path
// segment; escaping it keeps the request addressed at the project rather than at
// a route the site does not have.
func TestProjectStatusesEscapesTheProjectKey(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "me@acme.com", "tok").ProjectStatuses(context.Background(), "A B"); err != nil {
		t.Fatalf("ProjectStatuses error: %v", err)
	}
	if want := "/rest/api/3/project/A%20B/statuses"; gotPath != want {
		t.Errorf("requested %q, want %q", gotPath, want)
	}
}

// A project whose workflows report nothing is not an error: the editor renders
// its config-only fallback rather than a failure.
func TestProjectStatusesEmptyProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"1","name":"Task","statuses":[]}]`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "me@acme.com", "tok").ProjectStatuses(context.Background(), "PROJ")
	if err != nil {
		t.Fatalf("ProjectStatuses error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ProjectStatuses = %+v, want no statuses", got)
	}
}

func TestProjectStatusesSurfacesUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "me@acme.com", "tok").ProjectStatuses(context.Background(), "PROJ"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("ProjectStatuses err = %v, want ErrUnauthorized", err)
	}
}
