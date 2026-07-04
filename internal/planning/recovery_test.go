package planning

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

// countingRunner returns results[i] for call i (clamped to the last entry) and
// counts its calls, so a test can assert retry/fallback behavior at the
// orchestrator seam. On a nil (success) result it returns final as the payload;
// error calls ignore it, since the round never parses them.
type countingRunner struct {
	results []error
	final   string
	name    string
	calls   int
}

func (r *countingRunner) Run(context.Context, string, string) (agent.Result, error) {
	i := r.calls
	r.calls++
	if i >= len(r.results) {
		i = len(r.results) - 1
	}
	final := r.final
	if final == "" {
		final = `{"status":"prd","prd":{"title":"W","markdown":"# W\n\nbody"}}`
	}
	return agent.Result{Final: final}, r.results[i]
}

func (r *countingRunner) Provider() string { return r.name }

// TestRunRoundRetriesTransientThenSucceeds is the planning self-heal: a round that
// fails transiently on a fresh process is retried, not surfaced, and a later
// attempt that succeeds completes the round normally.
func TestRunRoundRetriesTransientThenSucceeds(t *testing.T) {
	r := &countingRunner{results: []error{errors.New("boom"), errors.New("boom"), nil}, name: "claude"}
	o := NewOrchestrator(r, t.TempDir()).WithClock(fixedClock()).WithRecovery(2, 0, nil)

	rr, err := o.RunRound(context.Background(), "an idea")
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if r.calls != 3 {
		t.Fatalf("want 3 attempts (2 retries), got %d", r.calls)
	}
	if rr.Payload.Status != StatusPRD {
		t.Fatalf("payload status = %q, want prd", rr.Payload.Status)
	}
	if got := rr.Session.Phase(); got != PhaseReview {
		t.Errorf("phase = %q, want %q", got, PhaseReview)
	}
}

// TestRunRoundRateLimitPauses pins the blameless path: a provider rate/usage limit
// pauses immediately, never consuming the transient retry budget, and leaves the
// fresh session at drafting — no question round was asked.
func TestRunRoundRateLimitPauses(t *testing.T) {
	r := &countingRunner{results: []error{errors.New("kimi run (plan): 429 usage limit reached")}, name: "kimi"}
	o := NewOrchestrator(r, t.TempDir()).WithClock(fixedClock()).WithRecovery(3, 0, nil)

	rr, err := o.RunRound(context.Background(), "an idea")
	if !IsPaused(err) {
		t.Fatalf("want a *PausedError, got %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("a rate limit must not be retried; got %d calls", r.calls)
	}
	var pe *PausedError
	errors.As(err, &pe)
	if pe.Provider != "kimi" {
		t.Errorf("pause provider = %q, want kimi", pe.Provider)
	}
	if got := rr.Session.Phase(); got != PhaseDrafting {
		t.Errorf("phase = %q, want drafting (no round consumed)", got)
	}
}

// TestRunRoundAuthPauses is the auth-wall guard: an ErrAuthRequired pauses
// blamelessly on the first hit — every retry re-hits the same wall — attributing
// the provider so the human knows what to re-authenticate.
func TestRunRoundAuthPauses(t *testing.T) {
	authErr := fmt.Errorf("claude interactive run (plan): %w", agent.ErrAuthRequired)
	r := &countingRunner{results: []error{authErr}, name: "claude"}
	o := NewOrchestrator(r, t.TempDir()).WithClock(fixedClock()).WithRecovery(3, 0, nil)

	_, err := o.RunRound(context.Background(), "an idea")
	if !IsPaused(err) {
		t.Fatalf("want a *PausedError, got %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("an auth wall must not be retried; got %d calls", r.calls)
	}
	var pe *PausedError
	errors.As(err, &pe)
	if pe.Provider != "claude" {
		t.Errorf("pause provider = %q, want claude", pe.Provider)
	}
}

// TestRunRoundFallsBackToNextProvider checks the provider-fallback seam: once the
// primary's retries are spent the round runs on the next provider, and its success
// completes the round.
func TestRunRoundFallsBackToNextProvider(t *testing.T) {
	primary := &countingRunner{results: []error{errors.New("boom")}, name: "claude"}
	fb := &countingRunner{results: []error{nil}, name: "codex"}
	o := NewOrchestrator(primary, t.TempDir()).WithClock(fixedClock()).
		WithRecovery(1, 0, []agent.Runner{fb})

	rr, err := o.RunRound(context.Background(), "an idea")
	if err != nil {
		t.Fatalf("want fallback success, got %v", err)
	}
	if primary.calls != 2 {
		t.Fatalf("primary: want 2 attempts (1 retry), got %d", primary.calls)
	}
	if fb.calls != 1 {
		t.Fatalf("fallback: want 1 attempt, got %d", fb.calls)
	}
	if rr.Payload.Status != StatusPRD {
		t.Fatalf("payload status = %q, want prd", rr.Payload.Status)
	}
}

// TestAnswerRoundPauseKeepsQuestionRound is the acceptance guard: a transient
// provider limit while asking the NEXT round pauses without consuming a question
// round. The answered round persists on the transcript, no extra round is recorded,
// and the checkpoint stays at questions so a later resume re-asks the same round.
func TestAnswerRoundPauseKeepsQuestionRound(t *testing.T) {
	questions := `{"status":"questions","questions":[{"id":"q1","text":"scope?","kind":"single","options":[{"label":"a"},{"label":"b"}]}]}`
	r := &countingRunner{results: []error{nil, errors.New("429 rate limit reached")}, final: questions, name: "claude"}
	o := NewOrchestrator(r, t.TempDir()).WithClock(fixedClock()).WithRecovery(3, 0, nil)

	rr, err := o.RunRound(context.Background(), "an idea")
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	sess := rr.Session
	if got := sess.Phase(); got != PhaseQuestions {
		t.Fatalf("round 1 phase = %q, want questions", got)
	}

	rr, err = o.AnswerRound(context.Background(), sess, []Answer{{ID: "q1", Values: []string{"a"}}})
	if !IsPaused(err) {
		t.Fatalf("answer round: want a *PausedError, got %v", err)
	}
	if r.calls != 2 {
		t.Fatalf("want 2 calls (round 1 + one paused attempt), got %d", r.calls)
	}

	transcript, err := sess.Transcript()
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	if len(transcript) != 1 {
		t.Fatalf("transcript holds %d rounds, want the one answered round", len(transcript))
	}
	if len(transcript[0].Answers) != 1 || transcript[0].Answers[0].ID != "q1" {
		t.Errorf("answered round not persisted: %+v", transcript[0].Answers)
	}
	if got := rr.Session.Phase(); got != PhaseQuestions {
		t.Errorf("phase = %q, want questions (round resumable)", got)
	}
}
