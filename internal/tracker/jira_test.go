package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
)

func TestJiraShouldFallback(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil never falls back", nil, false},
		{"not enabled falls back to MCP", jiraapi.ErrNotEnabled, true},
		{"unauthorized falls back to MCP", jiraapi.ErrUnauthorized, true},
		{"wrapped unauthorized still falls back", fmt.Errorf("title: %w", jiraapi.ErrUnauthorized), true},
		{"not found is surfaced", jiraapi.ErrNotFound, false},
		{"generic error is surfaced", errors.New("boom"), false},
	}
	for _, tc := range tests {
		if got := jiraShouldFallback(tc.err); got != tc.want {
			t.Errorf("%s: jiraShouldFallback(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// With no API token the direct path is disabled (ErrNotEnabled), so Title must
// fall back to the MCP runner and parse its TITLE= sentinel.
func TestJiraTitleFallsBackToRunner(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"title": {Final: "TITLE=Fix the widget"},
	}}
	j := &Jira{Runner: runner, Team: "PROJ"}

	got, err := j.Title(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("Title returned error: %v", err)
	}
	if got != "Fix the widget" {
		t.Errorf("Title = %q, want %q", got, "Fix the widget")
	}
	if runner.calls["title"] != 1 {
		t.Errorf("expected exactly one MCP title lookup, got %d", runner.calls["title"])
	}
}

// With a token set, Title resolves via the REST API and never touches the runner.
func TestJiraTitleUsesAPIWhenTokenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-7","fields":{"summary":"Fix the widget"}}`))
	}))
	defer srv.Close()

	runner := &recordingRunner{responses: map[string]agent.Result{}}
	j := &Jira{Runner: runner, Team: "PROJ", BaseURL: srv.URL, Email: "me@acme.com", APIToken: "tok"}

	got, err := j.Title(context.Background(), "PROJ-7")
	if err != nil {
		t.Fatalf("Title returned error: %v", err)
	}
	if got != "Fix the widget" {
		t.Errorf("Title = %q, want %q", got, "Fix the widget")
	}
	if runner.calls["title"] != 0 {
		t.Errorf("expected no MCP fallback when the API answers, got %d title calls", runner.calls["title"])
	}
}
