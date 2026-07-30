package jiraapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestTransitionsDisabledWithoutToken(t *testing.T) {
	if _, err := New("", "", "").Transitions(context.Background(), "PROJ-7"); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Transitions err = %v, want ErrNotEnabled", err)
	}
}

func TestApplyTransitionDisabledWithoutToken(t *testing.T) {
	if err := New("", "", "").ApplyTransition(context.Background(), "PROJ-7", "31", "", ""); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("ApplyTransition err = %v, want ErrNotEnabled", err)
	}
}

// Transitions reports each destination with its statusCategory, which is what
// lets a caller resolve a lifecycle stage on a workflow that names its statuses
// nothing like the loop does.
func TestTransitionsReportsDestinationsAndCategories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"transitions":[
			{"id":"11","name":"Start","to":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}},
			{"id":"21","name":"QA","to":{"name":"READY FOR QA","statusCategory":{"key":"indeterminate"}}},
			{"id":"31","name":"Finish","to":{"name":"Done","statusCategory":{"key":"done"}}}
		]}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "me@acme.com", "tok").Transitions(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("Transitions error: %v", err)
	}
	want := []Transition{
		{ID: "11", Name: "Start", Status: Status{Name: "In Progress", Category: "indeterminate"}},
		{ID: "21", Name: "QA", Status: Status{Name: "READY FOR QA", Category: "indeterminate"}},
		{ID: "31", Name: "Finish", Status: Status{Name: "Done", Category: "done"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Transitions = %+v, want %+v", got, want)
	}
}

// A transition with no destination status falls back to its own name, so the
// caller still has something to match and to name in an error.
func TestTransitionDestinationFallsBackToTransitionName(t *testing.T) {
	tr := Transition{ID: "41", Name: "Close"}
	if got := tr.Destination(); got != "Close" {
		t.Errorf("Destination() = %q, want Close", got)
	}
}

// ApplyTransition POSTs the transition id with the optional resolution and an ADF
// comment.
func TestApplyTransitionPostsResolutionAndComment(t *testing.T) {
	var post transitionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &post)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := New(srv.URL, "me@acme.com", "tok").ApplyTransition(context.Background(), "PROJ-7", "31", "Done", "Loop finished")
	if err != nil {
		t.Fatalf("ApplyTransition error: %v", err)
	}
	if post.Transition.ID != "31" {
		t.Errorf("transition id = %q, want 31", post.Transition.ID)
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

// With no resolution or comment, fields and update are omitted from the body.
func TestApplyTransitionWithoutResolutionOrComment(t *testing.T) {
	var post transitionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &post)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := New(srv.URL, "me@acme.com", "tok").ApplyTransition(context.Background(), "PROJ-7", "41", "", ""); err != nil {
		t.Fatalf("ApplyTransition error: %v", err)
	}
	if post.Transition.ID != "41" {
		t.Errorf("transition id = %q, want 41", post.Transition.ID)
	}
	if post.Fields != nil || post.Update != nil {
		t.Errorf("no resolution/comment expected: fields=%+v update=%+v", post.Fields, post.Update)
	}
}
