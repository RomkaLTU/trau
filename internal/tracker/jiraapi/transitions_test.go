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

// buildADF must round-trip back to its input through adfToText for plain,
// possibly multi-line strings (the ACs' "valid doc/paragraph/text JSON").
func TestBuildADFRoundTrips(t *testing.T) {
	cases := []string{
		"Single line",
		"First line\nSecond line",
		"Trau loop reset PROJ-1 to start fresh.",
	}
	for _, in := range cases {
		raw, err := json.Marshal(buildADF(in))
		if err != nil {
			t.Fatalf("marshal buildADF(%q): %v", in, err)
		}
		if got := adfToText(raw); got != in {
			t.Errorf("round-trip: adfToText(buildADF(%q)) = %q", in, got)
		}
	}
}

func TestBuildADFShape(t *testing.T) {
	raw, err := json.Marshal(buildADF("Hello"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Hello"}]}]}`
	if string(raw) != want {
		t.Errorf("buildADF JSON = %s, want %s", raw, want)
	}
}

func TestSetStatusDisabledWithoutToken(t *testing.T) {
	if err := New("", "", "").SetStatus(context.Background(), "PROJ-7", "Done", "", ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("SetStatus err = %v, want ErrNotEnabled", err)
	}
}

// SetStatus resolves the transition id by matching the target status name
// (case-insensitively, against to.name) and POSTs it with the optional
// resolution and an ADF comment.
func TestSetStatusResolvesTransitionAndPosts(t *testing.T) {
	var methods []string
	var post transitionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"transitions":[
				{"id":"11","name":"Start","to":{"name":"In Progress"}},
				{"id":"31","name":"Finish","to":{"name":"Done"}}
			]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &post)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := New(srv.URL, "me@acme.com", "tok").SetStatus(context.Background(), "PROJ-7", "done", "Done", "Loop finished")
	if err != nil {
		t.Fatalf("SetStatus error: %v", err)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Errorf("methods = %v, want [GET POST]", methods)
	}
	if post.Transition.ID != "31" {
		t.Errorf("transition id = %q, want 31 (Done, matched case-insensitively)", post.Transition.ID)
	}
	if post.Fields == nil || post.Fields.Resolution == nil || post.Fields.Resolution.Name != "Done" {
		t.Errorf("resolution not attached: %+v", post.Fields)
	}
	if post.Update == nil || len(post.Update.Comment) != 1 {
		t.Fatalf("comment not attached: %+v", post.Update)
	}
	raw, _ := json.Marshal(post.Update.Comment[0].Add.Body)
	if got := adfToText(raw); got != "Loop finished" {
		t.Errorf("comment body = %q, want %q", got, "Loop finished")
	}
}

// A workflow may name the transition after the target while its destination
// status differs; matching falls back to the transition's own name. With no
// resolution or comment, fields and update are omitted from the body.
func TestSetStatusMatchesTransitionName(t *testing.T) {
	var post transitionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"transitions":[{"id":"41","name":"Done","to":{"name":"Closed"}}]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &post)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := New(srv.URL, "me@acme.com", "tok").SetStatus(context.Background(), "PROJ-7", "Done", "", ""); err != nil {
		t.Fatalf("SetStatus error: %v", err)
	}
	if post.Transition.ID != "41" {
		t.Errorf("transition id = %q, want 41 (matched on transition name)", post.Transition.ID)
	}
	if post.Fields != nil || post.Update != nil {
		t.Errorf("no resolution/comment expected: fields=%+v update=%+v", post.Fields, post.Update)
	}
}

func TestSetStatusUnknownStatusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"transitions":[{"id":"11","name":"Start","to":{"name":"In Progress"}}]}`))
	}))
	defer srv.Close()

	err := New(srv.URL, "me@acme.com", "tok").SetStatus(context.Background(), "PROJ-7", "Done", "", "")
	if !errors.Is(err, ErrNoTransition) {
		t.Fatalf("SetStatus err = %v, want ErrNoTransition", err)
	}
	if !strings.Contains(err.Error(), "In Progress") {
		t.Errorf("error should name the available statuses, got %q", err.Error())
	}
}
