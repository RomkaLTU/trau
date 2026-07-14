package jiraapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// UpdateLabels sends incremental add/remove ops in one PUT, so a label change
// touches only the named labels and never rewrites the whole set.
func TestUpdateLabelsAddRemove(t *testing.T) {
	var (
		method string
		path   string
		req    issueUpdateRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := New(srv.URL, "me@acme.com", "tok").UpdateLabels(context.Background(), "PROJ-7", []string{"ready"}, []string{"quarantine"})
	if err != nil {
		t.Fatalf("UpdateLabels error: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %q, want PUT", method)
	}
	if path != "/rest/api/3/issue/PROJ-7" {
		t.Errorf("path = %q, want /rest/api/3/issue/PROJ-7", path)
	}
	want := []labelOp{{Add: "ready"}, {Remove: "quarantine"}}
	if len(req.Update.Labels) != len(want) {
		t.Fatalf("label ops = %+v, want %+v", req.Update.Labels, want)
	}
	for i, op := range want {
		if req.Update.Labels[i] != op {
			t.Errorf("op[%d] = %+v, want %+v", i, req.Update.Labels[i], op)
		}
	}
}

// An empty op set (all names blank after trimming) makes no HTTP call.
func TestUpdateLabelsNoOpWhenEmpty(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := New(srv.URL, "me@acme.com", "tok").UpdateLabels(context.Background(), "PROJ-7", []string{" "}, nil); err != nil {
		t.Fatalf("UpdateLabels error: %v", err)
	}
	if hits != 0 {
		t.Errorf("expected no HTTP call for an empty op set, got %d", hits)
	}
}

func TestUpdateLabelsDisabledWithoutToken(t *testing.T) {
	if err := New("", "", "").UpdateLabels(context.Background(), "PROJ-7", []string{"ready"}, nil); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("UpdateLabels err = %v, want ErrNotEnabled", err)
	}
}

// UpdateDescription PUTs the issue with an ADF description that round-trips back to
// the plain text, the fields-verb whole-value set CreateIssue uses.
func TestUpdateDescriptionPutsADF(t *testing.T) {
	var (
		method string
		path   string
		req    descriptionUpdateRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := New(srv.URL, "me@acme.com", "tok").UpdateDescription(context.Background(), "PROJ-7", "Line one\nLine two")
	if err != nil {
		t.Fatalf("UpdateDescription error: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %q, want PUT", method)
	}
	if path != "/rest/api/3/issue/PROJ-7" {
		t.Errorf("path = %q, want /rest/api/3/issue/PROJ-7", path)
	}
	raw, _ := json.Marshal(req.Fields.Description)
	if got := adfToText(raw); got != "Line one\nLine two" {
		t.Errorf("description = %q, want two lines", got)
	}
}

func TestUpdateDescriptionDisabledWithoutToken(t *testing.T) {
	if err := New("", "", "").UpdateDescription(context.Background(), "PROJ-7", "body"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("UpdateDescription err = %v, want ErrNotEnabled", err)
	}
}

// AddComment posts the standalone comment endpoint with an ADF body that
// round-trips back to the plain text.
func TestAddCommentPostsADF(t *testing.T) {
	var (
		method string
		path   string
		req    commentRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	err := New(srv.URL, "me@acme.com", "tok").AddComment(context.Background(), "PROJ-7", "Trau loop stopped: boom")
	if err != nil {
		t.Fatalf("AddComment error: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if path != "/rest/api/3/issue/PROJ-7/comment" {
		t.Errorf("path = %q, want /rest/api/3/issue/PROJ-7/comment", path)
	}
	raw, _ := json.Marshal(req.Body)
	if got := adfToText(raw); got != "Trau loop stopped: boom" {
		t.Errorf("comment body = %q, want %q", got, "Trau loop stopped: boom")
	}
}

// LinkBlocks POSTs a "Blocks" link with the blocker as the outward issue and the
// blocked sibling as the inward issue — the direction blockersFromLinks reads back.
func TestLinkBlocksPostsBlocksLink(t *testing.T) {
	var (
		method string
		path   string
		req    issueLinkRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	if err := New(srv.URL, "me@acme.com", "tok").LinkBlocks(context.Background(), "PROJ-1", "PROJ-2"); err != nil {
		t.Fatalf("LinkBlocks error: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if path != "/rest/api/3/issueLink" {
		t.Errorf("path = %q, want /rest/api/3/issueLink", path)
	}
	if req.Type.Name != "Blocks" {
		t.Errorf("link type = %q, want Blocks", req.Type.Name)
	}
	if req.OutwardIssue.Key != "PROJ-1" {
		t.Errorf("outward (blocker) = %q, want PROJ-1", req.OutwardIssue.Key)
	}
	if req.InwardIssue.Key != "PROJ-2" {
		t.Errorf("inward (blocked) = %q, want PROJ-2", req.InwardIssue.Key)
	}
}

func TestLinkBlocksDisabledWithoutToken(t *testing.T) {
	if err := New("https://acme.atlassian.net", "me@acme.com", "").LinkBlocks(context.Background(), "PROJ-1", "PROJ-2"); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("LinkBlocks without token = %v, want ErrNotEnabled", err)
	}
}

// CreateIssue resolves the issue type id via createmeta, then POSTs the issue
// with the resolved id, the project key, an ADF description and the labels,
// returning the new key.
func TestCreateIssueResolvesTypeAndPosts(t *testing.T) {
	var (
		methods []string
		req     createIssueRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			if !strings.Contains(r.URL.Path, "/issue/createmeta/PROJ/issuetypes") {
				t.Errorf("createmeta path = %q", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"values":[{"id":"10001","name":"Task"},{"id":"10004","name":"Bug"}]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"10500","key":"PROJ-500"}`))
	}))
	defer srv.Close()

	key, err := New(srv.URL, "me@acme.com", "tok").CreateIssue(context.Background(), "PROJ", "Bug", "It broke", "Line one\nLine two", []string{"HITL"}, "")
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if key != "PROJ-500" {
		t.Errorf("key = %q, want PROJ-500", key)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Errorf("methods = %v, want [GET POST]", methods)
	}
	if req.Fields.Project.Key != "PROJ" {
		t.Errorf("project key = %q, want PROJ", req.Fields.Project.Key)
	}
	if req.Fields.Parent != nil {
		t.Errorf("parent = %+v, want the field omitted for an empty parent", req.Fields.Parent)
	}
	if req.Fields.IssueType.ID != "10004" {
		t.Errorf("issuetype id = %q, want 10004 (Bug)", req.Fields.IssueType.ID)
	}
	if req.Fields.Summary != "It broke" {
		t.Errorf("summary = %q, want It broke", req.Fields.Summary)
	}
	if len(req.Fields.Labels) != 1 || req.Fields.Labels[0] != "HITL" {
		t.Errorf("labels = %v, want [HITL]", req.Fields.Labels)
	}
	raw, _ := json.Marshal(req.Fields.Description)
	if got := adfToText(raw); got != "Line one\nLine two" {
		t.Errorf("description = %q, want two lines", got)
	}
}

// A non-empty parent lands as the unified parent field on the create POST, so
// the issue nests under its epic at creation time.
func TestCreateIssueSetsParent(t *testing.T) {
	var req createIssueRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"values":[{"id":"10001","name":"Task"}]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"10501","key":"PROJ-501"}`))
	}))
	defer srv.Close()

	key, err := New(srv.URL, "me@acme.com", "tok").CreateIssue(context.Background(), "PROJ", "Task", "Child", "body", nil, "PROJ-500")
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if key != "PROJ-501" {
		t.Errorf("key = %q, want PROJ-501", key)
	}
	if req.Fields.Parent == nil || req.Fields.Parent.Key != "PROJ-500" {
		t.Errorf("parent = %+v, want key PROJ-500", req.Fields.Parent)
	}
}

// An issue type the project lacks is a real error, surfaced to the caller.
func TestCreateIssueUnknownType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"values":[{"id":"10001","name":"Task"}]}`))
			return
		}
		t.Error("must not POST when the issue type is unresolved")
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "me@acme.com", "tok").CreateIssue(context.Background(), "PROJ", "Bug", "s", "d", nil, ""); err == nil {
		t.Fatal("CreateIssue with an unknown type should error, got nil")
	}
}

func TestCreateIssueDisabled(t *testing.T) {
	if _, err := New("", "", "").CreateIssue(context.Background(), "PROJ", "Bug", "s", "d", nil, ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("no token: err = %v, want ErrNotEnabled", err)
	}
	if _, err := New("https://x.atlassian.net", "me@acme.com", "tok").CreateIssue(context.Background(), " ", "Bug", "s", "d", nil, ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("empty project: err = %v, want ErrNotEnabled", err)
	}
}
