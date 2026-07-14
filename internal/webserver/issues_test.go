package webserver

import (
	"context"
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
type labelCall struct {
	id     string
	add    []string
	remove []string
}

type descriptionCall struct {
	id, body string
}

type fakeWriter struct {
	created      []tracker.IssueDraft
	comments     []commentCall
	descriptions []descriptionCall
	labels       []labelCall
	published    []tracker.DocumentDraft
	order        []string
	issue        tracker.NewIssue
	doc          tracker.PublishedDocument
	createErr    error
	commentErr   error
	descErr      error
	labelErr     error
	publishErr   error
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
	f.order = append(f.order, "comment")
	f.comments = append(f.comments, commentCall{id: id, body: body})
	return f.commentErr
}

func (f *fakeWriter) UpdateDescription(_ context.Context, id, body string) error {
	f.order = append(f.order, "description")
	f.descriptions = append(f.descriptions, descriptionCall{id: id, body: body})
	return f.descErr
}

func (f *fakeWriter) UpdateLabels(_ context.Context, id string, add, remove []string) error {
	f.order = append(f.order, "labels")
	f.labels = append(f.labels, labelCall{id: id, add: add, remove: remove})
	return f.labelErr
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
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
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
	s := New("1.2.3", "0.0.0.0", "s3cret", nil, false, testStores(t))
	fake := newFakeWriter()
	s.newWriter = func(config.Config) (tracker.Writer, error) { return fake, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	comment := postJSON(t, ts.URL+APIPrefix+"/repos/acme/runs/COD-9/comment", CommentRequest{Body: "x"})
	_ = comment.Body.Close()
	if comment.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated comment = %d, want 401 on an exposed bind", comment.StatusCode)
	}
	if len(fake.comments) != 0 {
		t.Errorf("token gate let a write through: comments=%d", len(fake.comments))
	}
}
