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

// scriptedRunner returns a different scripted result per call, recording each
// prompt so a test can assert what accumulated context each round re-read.
type scriptedRunner struct {
	finals  []string
	calls   int
	prompts []string
}

func (r *scriptedRunner) Run(ctx context.Context, prompt, label string) (agent.Result, error) {
	r.prompts = append(r.prompts, prompt)
	final := r.finals[r.calls]
	r.calls++
	return agent.Result{Final: final}, nil
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

// TestRunRoundQuestions checks a first-round questions payload advances the
// checkpoint to questions without persisting a PRD.
func TestRunRoundQuestions(t *testing.T) {
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
	if got := rr.Session.Phase(); got != PhaseQuestions {
		t.Errorf("phase = %q, want questions", got)
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

// TestMultiRoundToPRD is the orchestrator-level tracer for a full question
// conversation: a scripted fake Runner returns questions → questions → prd across
// three fresh processes. It asserts the transcript accumulates both answered
// rounds, that the third round re-reads them, that the round cap forces the PRD,
// and that the checkpoint progresses drafting → questions → prd_ready.
func TestMultiRoundToPRD(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{finals: []string{
		`{"status":"questions","questions":[{"id":"q1","text":"who is the actor?","kind":"single","options":[{"label":"admins"},{"label":"editors"}]}]}`,
		`{"status":"questions","questions":[{"id":"q2","text":"what to name it?","kind":"text"}]}`,
		`{"status":"prd","prd":{"title":"Widgets","markdown":"# Widgets\n\nbody"}}`,
	}}
	o := NewOrchestrator(runner, root).WithClock(fixedClock()).WithMaxRounds(2)

	rr, err := o.RunRound(context.Background(), "let users export widgets")
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if rr.Payload.Status != StatusQuestions {
		t.Fatalf("round 1 status = %q, want questions", rr.Payload.Status)
	}
	if got := rr.Session.Phase(); got != PhaseQuestions {
		t.Errorf("round 1 phase = %q, want questions", got)
	}
	sess := rr.Session

	rr, err = o.AnswerRound(context.Background(), sess, []Answer{
		{ID: "q1", Question: "who is the actor?", Values: []string{"admins", "editors"}},
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if rr.Payload.Status != StatusQuestions {
		t.Fatalf("round 2 status = %q, want questions", rr.Payload.Status)
	}
	if got := rr.Session.Phase(); got != PhaseQuestions {
		t.Errorf("round 2 phase = %q, want questions", got)
	}

	rr, err = o.AnswerRound(context.Background(), sess, []Answer{
		{ID: "q2", Question: "what to name it?", Values: []string{"Widgets"}, Skipped: true},
	})
	if err != nil {
		t.Fatalf("round 3: %v", err)
	}
	if rr.Payload.Status != StatusPRD {
		t.Fatalf("round 3 status = %q, want prd", rr.Payload.Status)
	}
	if got := rr.Session.Phase(); got != PhasePRDReady {
		t.Errorf("round 3 phase = %q, want prd_ready", got)
	}

	transcript, err := sess.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(transcript) != 2 {
		t.Fatalf("transcript has %d rounds, want 2", len(transcript))
	}
	if got := transcript[0].Answers[0].Values; len(got) != 2 || got[0] != "admins" {
		t.Errorf("round 1 answer = %v, want [admins editors]", got)
	}
	if !transcript[1].Answers[0].Skipped {
		t.Error("round 2 answer should be flagged skipped")
	}

	if len(runner.prompts) != 3 {
		t.Fatalf("ran %d rounds, want 3", len(runner.prompts))
	}
	last := runner.prompts[2]
	if !strings.Contains(last, "who is the actor?") || !strings.Contains(last, "what to name it?") {
		t.Error("final round did not re-read the accumulated transcript")
	}
	if !strings.Contains(last, "## Assumptions") {
		t.Error("capped final round should force PRD-with-assumptions")
	}
	if strings.Contains(last, `"status":"questions"`) {
		t.Error("capped final round should not offer the questions contract")
	}
}

// TestRoundCapRejectsQuestions checks that when the agent ignores the capped
// prompt and asks again, the orchestrator rejects the payload rather than
// surfacing yet another question round.
func TestRoundCapRejectsQuestions(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{finals: []string{
		`{"status":"questions","questions":[{"id":"q1","text":"scope?","kind":"single","options":[{"label":"a"},{"label":"b"}]}]}`,
		`{"status":"questions","questions":[{"id":"q2","text":"more?","kind":"single","options":[{"label":"a"},{"label":"b"}]}]}`,
	}}
	o := NewOrchestrator(runner, root).WithClock(fixedClock()).WithMaxRounds(1)

	rr, err := o.RunRound(context.Background(), "an idea")
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}

	_, err = o.AnswerRound(context.Background(), rr.Session, []Answer{{ID: "q1", Values: []string{"a"}}})
	if err == nil {
		t.Fatal("round 2 at the cap should reject a questions payload")
	}
	if !strings.Contains(err.Error(), "round cap") {
		t.Errorf("error = %v, want a round-cap rejection", err)
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
