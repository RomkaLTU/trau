package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker"
)

type commentCall struct {
	id, body string
}

// fakeWriter records the drafts and comments the handlers hand it, standing in
// for a real direct tracker client so the endpoints are asserted without any
// network.
type fakeWriter struct {
	created    []tracker.IssueDraft
	comments   []commentCall
	published  []tracker.DocumentDraft
	issue      tracker.NewIssue
	doc        tracker.PublishedDocument
	createErr  error
	commentErr error
	publishErr error
}

func newFakeWriter() *fakeWriter {
	return &fakeWriter{
		issue: tracker.NewIssue{Identifier: "COD-42", URL: "https://linear.app/acme/issue/COD-42"},
		doc:   tracker.PublishedDocument{URL: "https://linear.app/acme/document/prd-abc123", Kind: tracker.DocumentKindDocument},
	}
}

func (f *fakeWriter) CreateIssue(_ context.Context, d tracker.IssueDraft) (tracker.NewIssue, error) {
	f.created = append(f.created, d)
	if f.createErr != nil {
		return tracker.NewIssue{}, f.createErr
	}
	return f.issue, nil
}

func (f *fakeWriter) AddComment(_ context.Context, id, body string) error {
	f.comments = append(f.comments, commentCall{id: id, body: body})
	return f.commentErr
}

func (f *fakeWriter) PublishDocument(_ context.Context, d tracker.DocumentDraft) (tracker.PublishedDocument, error) {
	f.published = append(f.published, d)
	if f.publishErr != nil {
		return tracker.PublishedDocument{}, f.publishErr
	}
	return f.doc, nil
}

// issuesServer builds a server with one exited repo ("acme") and a Writer
// factory that returns fake (or writerErr when set). It returns the repo's runs
// dir so a test can seed a checkpoint for the comment path.
func issuesServer(t *testing.T, fake tracker.Writer, writerErr error) (string, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testRegistrationsAt(t, home))
	s.home = home
	s.newWriter = func(config.Config) (tracker.Writer, error) {
		if writerErr != nil {
			return nil, writerErr
		}
		return fake, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return runsDir, ts
}

func TestCreateIssueFilesAndReturnsLink(t *testing.T) {
	fake := newFakeWriter()
	_, ts := issuesServer(t, fake, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues", CreateIssueRequest{
		Title:       "Filed from the hub",
		Description: "some context",
		Labels:      []string{"ready-for-agent", "  ", ""},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var out CreatedIssue
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Identifier != "COD-42" || out.URL == "" {
		t.Errorf("response = %+v, want the created identifier and link", out)
	}
	if out.Provider != "linear" {
		t.Errorf("provider = %q, want linear (the resolved default)", out.Provider)
	}
	if len(fake.created) != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", len(fake.created))
	}
	draft := fake.created[0]
	if draft.Title != "Filed from the hub" || draft.Description != "some context" {
		t.Errorf("draft = %+v, want the posted title/description", draft)
	}
	if len(draft.Labels) != 1 || draft.Labels[0] != "ready-for-agent" {
		t.Errorf("labels = %v, want blanks dropped leaving [ready-for-agent]", draft.Labels)
	}
}

func TestCreateIssueUnderParent(t *testing.T) {
	fake := newFakeWriter()
	_, ts := issuesServer(t, fake, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues", CreateIssueRequest{
		Title:  "Sub-issue of the epic",
		Parent: "  COD-1  ",
	})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if len(fake.created) != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", len(fake.created))
	}
	if fake.created[0].Parent != "COD-1" {
		t.Errorf("draft parent = %q, want the trimmed COD-1 so the child nests under the epic", fake.created[0].Parent)
	}
}

func TestCreateIssueRequiresTitle(t *testing.T) {
	fake := newFakeWriter()
	_, ts := issuesServer(t, fake, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues", CreateIssueRequest{Title: "   "})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a blank title", res.StatusCode)
	}
	if len(fake.created) != 0 {
		t.Errorf("CreateIssue calls = %d, want 0 (never call the tracker on invalid input)", len(fake.created))
	}
}

func TestCreateIssueUnknownRepo(t *testing.T) {
	_, ts := issuesServer(t, newFakeWriter(), nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/nope/issues", CreateIssueRequest{Title: "x"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestCreateIssueWithoutCredentials(t *testing.T) {
	_, ts := issuesServer(t, nil, tracker.ErrWriterUnavailable)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues", CreateIssueRequest{Title: "x"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 when the repo has no direct tracker credentials", res.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["error"] == "" {
		t.Error("422 body missing a config hint")
	}
}

func TestCreateIssueTrackerFailureIsBadGateway(t *testing.T) {
	fake := newFakeWriter()
	fake.createErr = errors.New("linear: 500")
	_, ts := issuesServer(t, fake, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues", CreateIssueRequest{Title: "x"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when the tracker API errors", res.StatusCode)
	}
}

func TestCreateIssueRejectsNonPOST(t *testing.T) {
	_, ts := issuesServer(t, newFakeWriter(), nil)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/issues")
	if err != nil {
		t.Fatalf("GET issues: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", res.StatusCode)
	}
}

func TestRunCommentAddsToTicket(t *testing.T) {
	fake := newFakeWriter()
	runsDir, ts := issuesServer(t, fake, nil)
	seedCheckpoint(t, runsDir, "COD-9", map[string]string{"PHASE": "quarantined"})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-9/comment", CommentRequest{Body: "filing a follow-up"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if len(fake.comments) != 1 {
		t.Fatalf("AddComment calls = %d, want 1", len(fake.comments))
	}
	if fake.comments[0].id != "COD-9" || fake.comments[0].body != "filing a follow-up" {
		t.Errorf("comment = %+v, want it on COD-9 with the posted body", fake.comments[0])
	}
}

func TestRunCommentUnknownRun(t *testing.T) {
	fake := newFakeWriter()
	_, ts := issuesServer(t, fake, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-404/comment", CommentRequest{Body: "hi"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a run with no checkpoint", res.StatusCode)
	}
	if len(fake.comments) != 0 {
		t.Errorf("AddComment calls = %d, want 0", len(fake.comments))
	}
}

func TestRunCommentRequiresBody(t *testing.T) {
	fake := newFakeWriter()
	runsDir, ts := issuesServer(t, fake, nil)
	seedCheckpoint(t, runsDir, "COD-9", map[string]string{"PHASE": "quarantined"})
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-9/comment", CommentRequest{Body: "  "})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a blank comment", res.StatusCode)
	}
	if len(fake.comments) != 0 {
		t.Errorf("AddComment calls = %d, want 0", len(fake.comments))
	}
}

func TestIssueEndpointsRequireTokenWhenExposed(t *testing.T) {
	s := New("1.2.3", "0.0.0.0", "s3cret", nil, false, testRegistrations(t))
	fake := newFakeWriter()
	s.newWriter = func(config.Config) (tracker.Writer, error) { return fake, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	create := postJSON(t, ts.URL+APIPrefix+"/repos/acme/issues", CreateIssueRequest{Title: "x"})
	_ = create.Body.Close()
	if create.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated create = %d, want 401 on an exposed bind", create.StatusCode)
	}
	comment := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-9/comment", CommentRequest{Body: "x"})
	_ = comment.Body.Close()
	if comment.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated comment = %d, want 401 on an exposed bind", comment.StatusCode)
	}
	if len(fake.created) != 0 || len(fake.comments) != 0 {
		t.Errorf("token gate let a write through: created=%d comments=%d", len(fake.created), len(fake.comments))
	}
}
