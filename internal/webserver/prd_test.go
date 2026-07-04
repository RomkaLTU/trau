package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestPublishPRDReturnsLink(t *testing.T) {
	fake := newFakeWriter()
	_, ts := issuesServer(t, fake, nil)

	markdown := "# Title\n\nA PRD body with **markdown**.\n\n- one\n- two\n"
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/prd", PublishPRDRequest{
		Title:    "  Payments PRD  ",
		Markdown: markdown,
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var out PublishedPRD
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.URL == "" || out.Kind != tracker.DocumentKindDocument || out.Provider != "linear" {
		t.Errorf("response = %+v, want the document url, kind and resolved provider", out)
	}
	if len(fake.published) != 1 {
		t.Fatalf("PublishDocument calls = %d, want 1", len(fake.published))
	}
	draft := fake.published[0]
	if draft.Title != "Payments PRD" {
		t.Errorf("title = %q, want it trimmed", draft.Title)
	}
	if draft.Markdown != markdown {
		t.Errorf("markdown = %q, want it preserved byte-for-byte", draft.Markdown)
	}
}

func TestPublishPRDEchoesIssueFallback(t *testing.T) {
	fake := newFakeWriter()
	fake.doc = tracker.PublishedDocument{
		URL:        "https://acme.atlassian.net/browse/PROJ-7",
		Identifier: "PROJ-7",
		Kind:       tracker.DocumentKindIssue,
	}
	_, ts := issuesServer(t, fake, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/prd", PublishPRDRequest{Title: "PRD", Markdown: "body"})
	defer func() { _ = res.Body.Close() }()
	var out PublishedPRD
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Kind != tracker.DocumentKindIssue || out.Identifier != "PROJ-7" {
		t.Errorf("response = %+v, want the issue kind and its identifier passed through", out)
	}
}

func TestPublishPRDRequiresTitle(t *testing.T) {
	fake := newFakeWriter()
	_, ts := issuesServer(t, fake, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/prd", PublishPRDRequest{Title: "  ", Markdown: "body"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a blank title", res.StatusCode)
	}
	if len(fake.published) != 0 {
		t.Errorf("PublishDocument calls = %d, want 0", len(fake.published))
	}
}

func TestPublishPRDRequiresMarkdown(t *testing.T) {
	fake := newFakeWriter()
	_, ts := issuesServer(t, fake, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/prd", PublishPRDRequest{Title: "PRD", Markdown: "  \n "})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an empty body", res.StatusCode)
	}
	if len(fake.published) != 0 {
		t.Errorf("PublishDocument calls = %d, want 0", len(fake.published))
	}
}

func TestPublishPRDUnknownRepo(t *testing.T) {
	_, ts := issuesServer(t, newFakeWriter(), nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/nope/prd", PublishPRDRequest{Title: "PRD", Markdown: "body"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown repo", res.StatusCode)
	}
}

func TestPublishPRDWithoutCredentials(t *testing.T) {
	_, ts := issuesServer(t, nil, tracker.ErrWriterUnavailable)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/prd", PublishPRDRequest{Title: "PRD", Markdown: "body"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 when the repo has no direct tracker credentials", res.StatusCode)
	}
}

func TestPublishPRDTrackerFailureIsBadGateway(t *testing.T) {
	fake := newFakeWriter()
	fake.publishErr = errors.New("linear: 500")
	_, ts := issuesServer(t, fake, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/prd", PublishPRDRequest{Title: "PRD", Markdown: "body"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when the tracker API errors", res.StatusCode)
	}
}

func TestPublishPRDRejectsNonPOST(t *testing.T) {
	_, ts := issuesServer(t, newFakeWriter(), nil)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/prd")
	if err != nil {
		t.Fatalf("GET prd: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", res.StatusCode)
	}
}
