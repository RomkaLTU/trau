package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
)

// logRenderer is a console.Renderer that only captures Logf lines.
type logRenderer struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRenderer) Logf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, a...))
}

func (l *logRenderer) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

func (l *logRenderer) LoopDone(console.SessionSummary)    {}
func (l *logRenderer) Event(event.Event)                  {}
func (l *logRenderer) Spin(string) func()                 { return func() {} }
func (l *logRenderer) SetTicket(string)                   {}
func (l *logRenderer) SetTitle(string)                    {}
func (l *logRenderer) Activity(activity.Activity, string) {}
func (l *logRenderer) TicketDone(console.TicketResult)    {}
func (l *logRenderer) Wait()                              {}

func cleanupPromptFiles(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.Remove(handoffPath(id))
		_ = os.Remove(verifyPath(id))
		_ = os.Remove(rubricPath(id))
		_ = os.Remove(buildNotesPath(id))
	})
}

// TestTicketRunAppliesPromptOverrides drives full ticket runs through Process
// and pins the snapshot contract: the override map is fetched exactly once per
// run, an overridden phase prompt and shared fragment reach the agent, prompts
// without an override keep their built-in bodies, and an edit lands on the next
// run rather than mid-ticket.
func TestTicketRunAppliesPromptOverrides(t *testing.T) {
	id := "COD-99401"
	cleanupPromptFiles(t, id)

	r := &recordingRunner{}
	p := newTestPipeline(t, r, &fakeTracker{})
	fetches := 0
	overrides := map[string]string{
		"build":      "Custom build for {{.ID}} on {{.Branch}}.{{.CodeStyle}}",
		"code_style": " Custom style fragment.",
	}
	p.FetchPrompts = func(context.Context) (map[string]string, error) {
		fetches++
		return overrides, nil
	}

	_ = p.Process(context.Background(), id)

	if fetches != 1 {
		t.Fatalf("FetchPrompts ran %d times, want once per ticket run", fetches)
	}
	build := r.prompt("build")
	if !strings.Contains(build, "Custom build for "+id+" on ") {
		t.Errorf("build prompt did not use the override:\n%s", build)
	}
	if !strings.Contains(build, "Custom style fragment.") {
		t.Errorf("build prompt did not resolve the shared fragment through the override map:\n%s", build)
	}
	if h := r.prompt("handoff"); !strings.Contains(h, "Write a QA brief for "+id) {
		t.Errorf("handoff prompt lost its built-in body:\n%s", h)
	}

	overrides = map[string]string{"build": "Edited build for {{.ID}}."}
	_ = p.Process(context.Background(), id)

	if fetches != 2 {
		t.Fatalf("FetchPrompts ran %d times after a second run, want 2", fetches)
	}
	if build := r.prompt("build"); !strings.Contains(build, "Edited build for "+id) {
		t.Errorf("second run did not pick up the edited override:\n%s", build)
	}
}

// TestBrokenPromptOverrideFallsBackAndWarns pins the fail-closed contract for a
// stored override that no longer renders: the phase runs on the built-in
// default and the skip is surfaced as a durable prompt_override_skipped event
// naming the prompt — never a fault or pause.
func TestBrokenPromptOverrideFallsBackAndWarns(t *testing.T) {
	id := "COD-99402"
	cleanupPromptFiles(t, id)

	r := &recordingRunner{}
	p := newTestPipeline(t, r, &fakeTracker{})
	var buf bytes.Buffer
	p.Events = event.New(&buf)
	p.FetchPrompts = func(context.Context) (map[string]string, error) {
		return map[string]string{"build": "Broken {{.NoSuchField}} body"}, nil
	}

	_ = p.Process(context.Background(), id)

	if build := r.prompt("build"); !strings.Contains(build, "Implement "+id+" on branch") {
		t.Errorf("build prompt did not fall back to the built-in default:\n%s", build)
	}
	evs := kindEvents(t, &buf, event.KindPromptOverrideSkipped)
	if len(evs) != 1 {
		t.Fatalf("emitted %d prompt_override_skipped events, want exactly 1", len(evs))
	}
	if got := strField(evs[0].Fields, "prompt"); got != "build" {
		t.Errorf("prompt = %q, want %q", got, "build")
	}
	if got := strField(evs[0].Fields, "ticket"); got != id {
		t.Errorf("ticket = %q, want %q", got, id)
	}
}

// TestPromptFetchFailureRunsOnDefaults pins the hub-down contract: one fetch,
// one logged warning, and the run proceeds on built-in defaults — prompt
// resolution never becomes a second blocking hub dependency.
func TestPromptFetchFailureRunsOnDefaults(t *testing.T) {
	id := "COD-99403"
	cleanupPromptFiles(t, id)

	r := &recordingRunner{}
	p := newTestPipeline(t, r, &fakeTracker{})
	logs := &logRenderer{}
	p.Renderer = logs
	fetches := 0
	p.FetchPrompts = func(context.Context) (map[string]string, error) {
		fetches++
		return nil, errors.New("hub down")
	}

	_ = p.Process(context.Background(), id)

	if fetches != 1 {
		t.Fatalf("FetchPrompts ran %d times, want 1 — no retry loop on a fetch failure", fetches)
	}
	if build := r.prompt("build"); !strings.Contains(build, "Implement "+id+" on branch") {
		t.Errorf("build prompt did not fall back to the built-in default:\n%s", build)
	}
	if !logs.contains("prompt overrides unavailable") {
		t.Errorf("fetch failure was not logged:\n%s", strings.Join(logs.lines, "\n"))
	}
}
