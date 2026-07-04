package planning

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
)

// Orchestrator runs planning rounds under a plans root using the provider-agnostic
// agent Runner seam. It owns session creation and checkpoint progression; each
// round is a fresh isolated process routed by the "plan" phase — never a resumed
// session — exactly like every pipeline phase.
type Orchestrator struct {
	runner agent.Runner
	root   string
	now    func() time.Time
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

// RoundResult is the outcome of one planning round: the durable session it ran
// under and the validated payload the agent returned.
type RoundResult struct {
	Session *Session
	Payload Payload
}

// RunRound starts a durable plan session from idea (raw text, or a path to a file
// containing the idea), runs one planning round as a fresh agent process through
// the Runner seam, validates the returned payload, and — when the agent returns a
// PRD — persists it and advances the checkpoint to prd_ready. Other statuses are
// returned without a checkpoint advance; the rounds that act on them are later
// slices. The session is returned even on failure so a caller can surface where
// the work stopped.
func (o *Orchestrator) RunRound(ctx context.Context, idea string) (*RoundResult, error) {
	text, err := resolveIdea(idea)
	if err != nil {
		return nil, err
	}
	sess, err := newSession(o.root, text, o.now)
	if err != nil {
		return nil, err
	}
	res, err := o.runner.Run(ctx, BuildPrompt(text), agent.PhasePlan)
	if err != nil {
		return &RoundResult{Session: sess}, err
	}
	payload, err := Parse(res.Final)
	if err != nil {
		return &RoundResult{Session: sess}, err
	}
	if payload.Status == StatusPRD {
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
