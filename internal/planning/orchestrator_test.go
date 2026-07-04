package planning

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
)

// fakeRunner returns a scripted result/error for every call and records the last
// prompt and label so the orchestrator's use of the seam can be asserted.
type fakeRunner struct {
	final string
	err   error

	gotPrompt string
	gotLabel  string
}

func (r *fakeRunner) Run(ctx context.Context, prompt, label string) (agent.Result, error) {
	r.gotPrompt = prompt
	r.gotLabel = label
	return agent.Result{Final: r.final}, r.err
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC) }
}

// TestRunRoundPRD is the orchestrator-level tracer: idea → fake Runner returning a
// scripted prd payload → the checkpoint progresses to prd_ready and the PRD is
// persisted, with no real agent or TUI.
func TestRunRoundPRD(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{final: `{"status":"prd","prd":{"title":"Widget","markdown":"# Widget\n\nbody"}}`}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())

	rr, err := o.RunRound(context.Background(), "let users export widgets")
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	if runner.gotLabel != agent.PhasePlan {
		t.Errorf("round ran under label %q, want %q", runner.gotLabel, agent.PhasePlan)
	}
	if !strings.Contains(runner.gotPrompt, "let users export widgets") {
		t.Error("prompt did not carry the idea")
	}
	if rr.Payload.Status != StatusPRD {
		t.Fatalf("payload status = %q, want prd", rr.Payload.Status)
	}

	sess := rr.Session
	if got := sess.Phase(); got != PhasePRDReady {
		t.Errorf("checkpoint phase = %q, want %q", got, PhasePRDReady)
	}
	if got := strings.TrimSpace(sess.Idea()); got != "let users export widgets" {
		t.Errorf("idea snapshot = %q", got)
	}
	prd, ok := sess.PRD()
	if !ok {
		t.Fatal("PRD not persisted")
	}
	if prd.Title != "Widget" || !strings.Contains(prd.Markdown, "# Widget") {
		t.Errorf("persisted PRD = %+v", prd)
	}
	if _, err := os.Stat(filepath.Join(sess.Dir(), prdFile)); err != nil {
		t.Errorf("prd.md not on disk: %v", err)
	}
}

// TestRunRoundNonPRD checks the graceful path: a questions payload is returned
// without persisting a PRD, and the checkpoint stays at drafting.
func TestRunRoundNonPRD(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{final: `{"status":"questions","questions":[{"id":"q1","text":"scope?","options":[{"label":"a"}]}]}`}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())

	rr, err := o.RunRound(context.Background(), "an idea")
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if rr.Payload.Status != StatusQuestions {
		t.Fatalf("status = %q, want questions", rr.Payload.Status)
	}
	if got := rr.Session.Phase(); got != PhaseDrafting {
		t.Errorf("phase = %q, want drafting (no advance on non-prd)", got)
	}
	if _, ok := rr.Session.PRD(); ok {
		t.Error("PRD persisted for a questions payload")
	}
}

// TestRunRoundMalformed surfaces a parse failure while still creating the durable
// session, so the caller can point the user at where it stopped.
func TestRunRoundMalformed(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{final: "not json"}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())

	rr, err := o.RunRound(context.Background(), "an idea")
	if err == nil {
		t.Fatal("RunRound: want an error for malformed payload")
	}
	if rr == nil || rr.Session == nil {
		t.Fatal("session should be returned even on parse failure")
	}
	if got := rr.Session.Phase(); got != PhaseDrafting {
		t.Errorf("phase = %q, want drafting", got)
	}
}

func TestRunRoundRunnerError(t *testing.T) {
	root := t.TempDir()
	boom := errors.New("boom")
	o := NewOrchestrator(&fakeRunner{err: boom}, root).WithClock(fixedClock())

	rr, err := o.RunRound(context.Background(), "an idea")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if rr.Session.Phase() != PhaseDrafting {
		t.Errorf("phase = %q, want drafting", rr.Session.Phase())
	}
}

// TestRunRoundFileIdea reads the idea from a path when the input names a file.
func TestRunRoundFileIdea(t *testing.T) {
	root := t.TempDir()
	ideaPath := filepath.Join(t.TempDir(), "idea.txt")
	if err := os.WriteFile(ideaPath, []byte("idea from a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{final: `{"status":"prd","prd":{"title":"T","markdown":"x"}}`}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())

	rr, err := o.RunRound(context.Background(), ideaPath)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if got := strings.TrimSpace(rr.Session.Idea()); got != "idea from a file" {
		t.Errorf("idea snapshot = %q, want file contents", got)
	}
	if !strings.Contains(runner.gotPrompt, "idea from a file") {
		t.Error("prompt did not carry the file idea")
	}
}
