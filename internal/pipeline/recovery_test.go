package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

// countingRunner returns results[i] for call i (clamped to the last entry) and
// counts how many times it ran, so a test can assert retry/fallback behavior. The
// non-nil Final lets success-path assertions check the returned output.
type countingRunner struct {
	results []error
	name    string
	calls   int
}

func (r *countingRunner) Run(context.Context, string, string) (agent.Result, error) {
	i := r.calls
	r.calls++
	if i >= len(r.results) {
		i = len(r.results) - 1
	}
	return agent.Result{Final: "ok"}, r.results[i]
}

func (r *countingRunner) Provider() string { return r.name }

// TestRecoverStepRetriesTransientThenSucceeds is the core COD-583 self-heal: a
// step that fails transiently on a fresh process is retried, not parked, and a
// later attempt that succeeds completes the phase normally.
func TestRecoverStepRetriesTransientThenSucceeds(t *testing.T) {
	r := &countingRunner{results: []error{errors.New("boom"), errors.New("boom"), nil}, name: "claude"}
	p := newTestPipeline(t, r, &fakeTracker{})
	p.AgentRetries = 2
	p.AgentBackoff = 0

	out, err := p.agentStep(context.Background(), "COD-1", "build", "prompt")
	if err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if out != "ok" {
		t.Errorf("out = %q, want %q", out, "ok")
	}
	if r.calls != 3 {
		t.Fatalf("want 3 attempts (2 retries), got %d", r.calls)
	}
}

// TestRecoverStepRateLimitPausesWithoutRetry pins the unchanged blameless path: a
// provider rate/usage limit must pause immediately, never consuming the transient
// retry budget.
func TestRecoverStepRateLimitPausesWithoutRetry(t *testing.T) {
	r := &countingRunner{results: []error{errors.New("kimi run (build): 429 usage limit reached")}, name: "kimi"}
	p := newTestPipeline(t, r, &fakeTracker{})
	p.AgentRetries = 3
	p.AgentBackoff = 0

	_, err := p.agentStep(context.Background(), "COD-2", "build", "prompt")
	if !IsPaused(err) {
		t.Fatalf("want a *PausedError, got %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("a rate limit must not be retried; got %d calls", r.calls)
	}
}

// TestRecoverStepAuthFailurePausesWithoutRetry is the COD-596 guard: a provider
// auth/login wall (ErrAuthRequired) must pause blamelessly on the first hit —
// never consuming the transient retry budget, since every retry re-hits the same
// wall — and the pause must attribute the provider so the human knows what to
// re-authenticate.
func TestRecoverStepAuthFailurePausesWithoutRetry(t *testing.T) {
	authErr := fmt.Errorf("claude interactive run (build): %w", agent.ErrAuthRequired)
	r := &countingRunner{results: []error{authErr}, name: "claude"}
	p := newTestPipeline(t, r, &fakeTracker{})
	p.AgentRetries = 3
	p.AgentBackoff = 0

	_, err := p.agentStep(context.Background(), "COD-596", "build", "prompt")
	var pe *PausedError
	if !errors.As(err, &pe) {
		t.Fatalf("want a *PausedError, got %v", err)
	}
	if pe.Provider != "claude" {
		t.Errorf("pause provider = %q, want %q", pe.Provider, "claude")
	}
	if r.calls != 1 {
		t.Fatalf("an auth wall must not be retried; got %d calls", r.calls)
	}
}

// TestRecoverStepFallsBackToNextProvider checks the provider-fallback seam: once
// the primary's retries are exhausted, the same step runs on the next provider in
// the chain and its success completes the phase.
func TestRecoverStepFallsBackToNextProvider(t *testing.T) {
	primary := &countingRunner{results: []error{errors.New("boom")}, name: "claude"}
	fb := &countingRunner{results: []error{nil}, name: "codex"}
	p := newTestPipeline(t, primary, &fakeTracker{})
	p.AgentRetries = 1
	p.AgentBackoff = 0
	p.Fallback = func(string) []agent.Runner { return []agent.Runner{fb} }

	out, err := p.agentStep(context.Background(), "COD-3", "handoff", "prompt")
	if err != nil {
		t.Fatalf("want fallback success, got %v", err)
	}
	if out != "ok" {
		t.Errorf("out = %q, want %q", out, "ok")
	}
	if primary.calls != 2 {
		t.Fatalf("primary: want 2 attempts (1 retry), got %d", primary.calls)
	}
	if fb.calls != 1 {
		t.Fatalf("fallback: want 1 attempt, got %d", fb.calls)
	}
}

// TestRecoverStepExhaustedWrapsAttempts checks the exhausted path: every provider
// and retry fails transiently, the run does NOT pause, and the surfaced error
// wraps the last failure plus a summary of the attempts/providers tried (so the
// fault recap can name them) — distinct from a single-shot failure.
func TestRecoverStepExhaustedWrapsAttempts(t *testing.T) {
	primary := &countingRunner{results: []error{errors.New("boom")}, name: "claude"}
	last := errors.New("boom2")
	fb := &countingRunner{results: []error{last}, name: "codex"}
	p := newTestPipeline(t, primary, &fakeTracker{})
	p.AgentRetries = 1
	p.AgentBackoff = 0
	p.Fallback = func(string) []agent.Runner { return []agent.Runner{fb} }

	_, err := p.agentStep(context.Background(), "COD-4", "build", "prompt")
	if err == nil {
		t.Fatal("want an error after exhausting the chain")
	}
	if IsPaused(err) {
		t.Fatalf("transient exhaustion must not pause: %v", err)
	}
	if primary.calls != 2 || fb.calls != 2 {
		t.Fatalf("attempts: primary=%d fallback=%d, want 2 and 2", primary.calls, fb.calls)
	}
	if !errors.Is(err, last) {
		t.Errorf("err must wrap the last failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "exhausted recovery") || !strings.Contains(err.Error(), "2 provider(s)") {
		t.Errorf("err = %v, want it to summarize the attempts/providers tried", err)
	}
}
