package planning

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// defaultMaxRounds caps how many rounds of questions a plan session may ask
// before the agent is forced to draft the PRD with explicit assumptions. Small
// by design; overridden per-orchestrator from the MAX_PLAN_ROUNDS knob.
const defaultMaxRounds = 3

// Orchestrator runs planning rounds under a plans root using the provider-agnostic
// agent Runner seam. It owns session creation and checkpoint progression; each
// round is a fresh isolated process routed by the "plan" phase — never a resumed
// session — exactly like every pipeline phase. The "conversation" across rounds
// is the durable transcript on disk, not a live agent session.
type Orchestrator struct {
	runner    agent.Runner
	root      string
	now       func() time.Time
	maxRounds int

	retries     int
	backoffSecs int
	fallback    []agent.Runner
}

// NewOrchestrator returns an Orchestrator that roots plan sessions under root
// (typically <runs>/_plans) and runs rounds through runner.
func NewOrchestrator(runner agent.Runner, root string) *Orchestrator {
	return &Orchestrator{runner: runner, root: root, now: time.Now}
}

// WithClock overrides the session-id/timestamp clock; intended for tests.
func (o *Orchestrator) WithClock(now func() time.Time) *Orchestrator {
	o.now = now
	return o
}

// WithMaxRounds caps the number of question rounds a session may take before the
// PRD is forced. A value <= 0 keeps the default.
func (o *Orchestrator) WithMaxRounds(n int) *Orchestrator {
	o.maxRounds = n
	return o
}

func (o *Orchestrator) roundCap() int {
	if o.maxRounds <= 0 {
		return defaultMaxRounds
	}
	return o.maxRounds
}

// WithRecovery folds a planning round's agent call into the same transient-failure
// recovery the pipeline gives its phases: retries transient failures up to retries
// times on a fresh process, then falls back across the given providers, backing off
// backoffSecs*(attempt+1) between attempts. A provider rate/usage limit or auth wall
// short-circuits to a blameless [PausedError] instead — the session keeps its
// checkpoint and resumes once the limit clears, never burning a question round.
func (o *Orchestrator) WithRecovery(retries, backoffSecs int, fallback []agent.Runner) *Orchestrator {
	if retries < 0 {
		retries = 0
	}
	o.retries = retries
	o.backoffSecs = backoffSecs
	o.fallback = fallback
	return o
}

// PausedError marks a planning round stopped blamelessly on a provider rate/usage
// limit or auth wall. The session stays at its last checkpoint, so a later resume
// re-runs the round once the provider recovers — no question round is consumed.
type PausedError struct {
	Provider string
	Reason   string
}

func (e *PausedError) Error() string { return "planning paused: " + e.Reason }

// IsPaused reports whether err is (or wraps) a *PausedError.
func IsPaused(err error) bool {
	var pe *PausedError
	return errors.As(err, &pe)
}

// runRound drives a single round's agent call through bounded transient-failure
// recovery: the primary runner first, each retried on a fresh process up to
// o.retries times, then the configured fallback providers. A rate/usage limit or
// auth wall pauses immediately (never retried); an outer-context cancellation stops
// without burning retries. The last error surfaces when the whole chain is spent.
func (o *Orchestrator) runRound(ctx context.Context, prompt string) (agent.Result, error) {
	chain := []agent.Runner{o.runner}
	for _, r := range o.fallback {
		if r != nil {
			chain = append(chain, r)
		}
	}

	var lastErr error
	for _, runner := range chain {
		for attempt := 0; ; attempt++ {
			res, err := runner.Run(ctx, prompt, agent.PhasePlan)
			if err == nil {
				return res, nil
			}
			if agent.IsRateLimited(err) || agent.IsAuthRequired(err) {
				return res, pausedError(runner, err)
			}
			lastErr = err
			if ctx.Err() != nil {
				return res, err
			}
			if attempt >= o.retries {
				break
			}
			o.backoff(attempt)
		}
	}
	return agent.Result{}, lastErr
}

// backoff sleeps a growing delay before a transient retry: backoffSecs*(n+1)
// seconds. Zero backoffSecs is instant.
func (o *Orchestrator) backoff(n int) {
	if o.backoffSecs <= 0 {
		return
	}
	time.Sleep(time.Duration(o.backoffSecs*(n+1)) * time.Second)
}

// pausedError builds the blameless *PausedError for a round that hit a provider
// rate/usage limit or auth wall, attributing the provider so the surface can say
// what to wait on or re-authenticate.
func pausedError(runner agent.Runner, err error) error {
	prov := providerName(runner)
	if agent.IsAuthRequired(err) {
		return &PausedError{Provider: prov, Reason: prov + " authentication required — re-login"}
	}
	return &PausedError{Provider: prov, Reason: prov + " rate/usage limit reached"}
}

// providerName names the backend a runner dispatches to, for a pause message;
// backends and the Router implement Provider(). Defaults to "provider".
func providerName(runner agent.Runner) string {
	if pv, ok := runner.(interface{ Provider() string }); ok {
		if name := pv.Provider(); name != "" {
			return name
		}
	}
	return "provider"
}

// RoundResult is the outcome of one planning round: the durable session it ran
// under and the validated payload the agent returned.
type RoundResult struct {
	Session *Session
	Payload Payload
}

// RunRound starts a durable plan session from idea (raw text, or a path to a file
// containing the idea) and runs its first planning round. The session is returned
// even on failure so a caller can surface where the work stopped.
func (o *Orchestrator) RunRound(ctx context.Context, idea string) (*RoundResult, error) {
	text, err := resolveIdea(idea)
	if err != nil {
		return nil, err
	}
	sess, err := newSession(o.root, text, o.now)
	if err != nil {
		return nil, err
	}
	return o.round(ctx, sess)
}

// AnswerRound records answers to the previous round's questions on the session's
// durable transcript, then runs the next planning round as a fresh agent process
// that re-reads the accumulated idea + transcript. No agent session is resumed.
func (o *Orchestrator) AnswerRound(ctx context.Context, sess *Session, answers []Answer) (*RoundResult, error) {
	prior, err := sess.Transcript()
	if err != nil {
		return &RoundResult{Session: sess}, err
	}
	if err := sess.AppendRound(QARound{Round: len(prior) + 1, Answers: answers}); err != nil {
		return &RoundResult{Session: sess}, err
	}
	return o.round(ctx, sess)
}

// ReviseRound runs a fresh revision round after the user reviewed the drafted PRD
// and requested changes with a free-text note. It re-reads the idea, the settled
// Q&A transcript, and the current PRD from disk — no agent session is resumed —
// and carries the note into the prompt. The revised PRD replaces the durable copy,
// leaving the session at prd_review for another approve-or-revise pass. A revision
// must return a PRD; a questions or slices payload is rejected.
func (o *Orchestrator) ReviseRound(ctx context.Context, sess *Session, note string) (*RoundResult, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return &RoundResult{Session: sess}, fmt.Errorf("planning: empty change request")
	}
	prd, ok := sess.PRD()
	if !ok {
		return &RoundResult{Session: sess}, fmt.Errorf("planning: no PRD to revise")
	}
	transcript, err := sess.Transcript()
	if err != nil {
		return &RoundResult{Session: sess}, err
	}

	res, err := o.runRound(ctx, BuildRevisionPrompt(sess.Idea(), transcript, prd.Markdown, note))
	if err != nil {
		return &RoundResult{Session: sess}, err
	}
	payload, err := Parse(res.Final)
	if err != nil {
		return &RoundResult{Session: sess}, err
	}
	if payload.Status != StatusPRD {
		return &RoundResult{Session: sess, Payload: payload}, fmt.Errorf("planning: revision returned %q, want a revised prd", payload.Status)
	}
	if err := sess.savePRD(*payload.PRD); err != nil {
		return &RoundResult{Session: sess, Payload: payload}, fmt.Errorf("persist revised prd: %w", err)
	}
	return &RoundResult{Session: sess, Payload: payload}, nil
}

// PublishResult reports what publishing an approved PRD did with the tracker. Epic
// is the created epic identifier; Published is false when the tracker lacks the
// hierarchical-create capability and the plan stayed local at prd_ready.
type PublishResult struct {
	Epic      string
	Published bool
}

// Publish creates the tracker epic that carries an approved PRD as its description,
// advancing the checkpoint to published and recording the epic identifier. The epic
// is created without any ready label — it is a container, never a buildable leaf —
// and the tracker places it in its bound project so the ownership guard keeps
// holding. The durable local PRD copy is left in place. A tracker that lacks the
// hierarchical-create capability degrades gracefully: nothing is created, the
// session stays where it is (prd_ready), and the result reports the skip so the
// caller can surface it.
func (o *Orchestrator) Publish(ctx context.Context, sess *Session, tr tracker.Tracker) (PublishResult, error) {
	prd, ok := sess.PRD()
	if !ok {
		return PublishResult{}, fmt.Errorf("planning: no PRD to publish")
	}
	creator, ok := tr.(tracker.HierarchicalCreator)
	if !ok {
		return PublishResult{}, nil
	}
	epic, err := creator.CreateIssue(ctx, tracker.IssueSpec{Title: prd.Title, Description: prd.Markdown})
	if err != nil {
		return PublishResult{}, fmt.Errorf("publish epic: %w", err)
	}
	if err := sess.markPublished(epic); err != nil {
		return PublishResult{Epic: epic}, fmt.Errorf("checkpoint published: %w", err)
	}
	return PublishResult{Epic: epic, Published: true}, nil
}

// ResumeRound re-runs a session's current round as a fresh process, rebuilding
// the pending step from the idea and settled transcript alone — nothing already
// answered is replayed, and nothing unanswered was ever persisted to lose.
func (o *Orchestrator) ResumeRound(ctx context.Context, sess *Session) (*RoundResult, error) {
	return o.round(ctx, sess)
}

// round runs one planning round against the session's accumulated context: it
// builds the prompt from the idea and transcript, runs a fresh agent process,
// validates the payload, enforces the round cap, and checkpoints the outcome —
// PhaseQuestions for a questions payload, and a persisted PRD resting at
// prd_review for a PRD. At the cap the prompt forces a PRD, and a stray questions
// payload is rejected rather than asked.
func (o *Orchestrator) round(ctx context.Context, sess *Session) (*RoundResult, error) {
	transcript, err := sess.Transcript()
	if err != nil {
		return &RoundResult{Session: sess}, err
	}
	capped := len(transcript) >= o.roundCap()

	res, err := o.runRound(ctx, BuildPrompt(sess.Idea(), transcript, capped))
	if err != nil {
		return &RoundResult{Session: sess}, err
	}
	payload, err := Parse(res.Final)
	if err != nil {
		return &RoundResult{Session: sess}, err
	}

	if capped && payload.Status == StatusQuestions {
		return &RoundResult{Session: sess}, fmt.Errorf("planning: round cap of %d reached, question payload rejected", o.roundCap())
	}

	switch payload.Status {
	case StatusQuestions:
		if err := sess.setPhase(PhaseQuestions); err != nil {
			return &RoundResult{Session: sess, Payload: payload}, fmt.Errorf("checkpoint questions: %w", err)
		}
	case StatusPRD:
		if err := sess.savePRD(*payload.PRD); err != nil {
			return &RoundResult{Session: sess, Payload: payload}, fmt.Errorf("persist prd: %w", err)
		}
	}
	return &RoundResult{Session: sess, Payload: payload}, nil
}

// resolveIdea turns the Plan screen's input into idea text: a single-line input
// naming an existing regular file is read from disk; anything else — including any
// multi-line text — is the idea itself.
func resolveIdea(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", fmt.Errorf("empty idea")
	}
	if !strings.Contains(trimmed, "\n") {
		if info, err := os.Stat(trimmed); err == nil && info.Mode().IsRegular() {
			b, err := os.ReadFile(trimmed)
			if err != nil {
				return "", fmt.Errorf("read idea file %s: %w", trimmed, err)
			}
			if strings.TrimSpace(string(b)) == "" {
				return "", fmt.Errorf("idea file %s is empty", trimmed)
			}
			return string(b), nil
		}
	}
	return input, nil
}
