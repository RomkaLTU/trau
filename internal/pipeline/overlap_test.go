package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/state"
)

// overlapRunner dispatches by phase so a test can script the handoff, lintfix, and
// cleanup steps independently and assert how many times each ran — the calls map and
// cancelled set are mutex-guarded because the overlap runs two of them concurrently.
type overlapRunner struct {
	mu        sync.Mutex
	calls     map[string]int
	cancelled map[string]bool
	hooks     map[string]func(ctx context.Context) (agent.Result, error)
}

func (r *overlapRunner) Run(ctx context.Context, prompt, phase string) (agent.Result, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[phase]++
	r.mu.Unlock()
	if h := r.hooks[phase]; h != nil {
		return h(ctx)
	}
	return agent.Result{Final: phase + "-ok"}, nil
}

func (r *overlapRunner) count(phase string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[phase]
}

func (r *overlapRunner) markCancelled(phase string) {
	r.mu.Lock()
	if r.cancelled == nil {
		r.cancelled = map[string]bool{}
	}
	r.cancelled[phase] = true
	r.mu.Unlock()
}

func (r *overlapRunner) wasCancelled(phase string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled[phase]
}

// writeBriefHook returns a handoff hook that drops a non-empty brief so handoffWork's
// existence check passes, then reports success.
func writeBriefHook(id string) func(context.Context) (agent.Result, error) {
	return func(context.Context) (agent.Result, error) {
		if err := os.WriteFile(handoffPath(id), []byte("QA brief: check the thing.\n"), 0o644); err != nil {
			return agent.Result{}, err
		}
		return agent.Result{Final: "handoff-ok"}, nil
	}
}

// blockUntilCancelledHook blocks until ctx is cancelled (recording it), or fails the
// side after a generous timeout so a broken cancellation can't hang the test.
func (r *overlapRunner) blockUntilCancelledHook(phase string) func(context.Context) (agent.Result, error) {
	return func(ctx context.Context) (agent.Result, error) {
		select {
		case <-ctx.Done():
			r.markCancelled(phase)
			return agent.Result{}, ctx.Err()
		case <-time.After(2 * time.Second):
			return agent.Result{}, errors.New(phase + ": never cancelled")
		}
	}
}

func cleanTmp(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.Remove(handoffPath(id))
		_ = os.Remove(verifyPath(id))
		_ = os.Remove(rubricPath(id))
	})
}

// TestHandoffAndCleanupOverlaps proves the two sides run concurrently: handoff and
// lintfix each wait for the other to have started before finishing, so a sequential
// pipeline would deadlock the barrier and time out. Both sides complete, cleanup runs
// (no tiny-diff skip), the handed_off checkpoint lands, and each phase records its own
// transcript without clobbering the others.
func TestHandoffAndCleanupOverlaps(t *testing.T) {
	id := "COD-79800"
	cleanTmp(t, id)

	handoffIn := make(chan struct{})
	lintfixIn := make(chan struct{})
	r := &overlapRunner{hooks: map[string]func(context.Context) (agent.Result, error){
		"handoff": func(ctx context.Context) (agent.Result, error) {
			close(handoffIn)
			select {
			case <-lintfixIn:
			case <-time.After(2 * time.Second):
				return agent.Result{}, errors.New("handoff: lintfix never started (no overlap)")
			}
			if err := os.WriteFile(handoffPath(id), []byte("QA brief\n"), 0o644); err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Final: "handoff-ok"}, nil
		},
		"lintfix": func(ctx context.Context) (agent.Result, error) {
			close(lintfixIn)
			select {
			case <-handoffIn:
			case <-time.After(2 * time.Second):
				return agent.Result{}, errors.New("lintfix: handoff never started (no overlap)")
			}
			return agent.Result{Final: "lintfix-ok"}, nil
		},
	}}
	p := newTestPipeline(t, r, &fakeTracker{})
	p.LintFix = true
	p.Cleanup = true

	if err := p.handoffAndCleanup(context.Background(), id, true); err != nil {
		t.Fatalf("handoffAndCleanup: %v", err)
	}

	for _, phase := range []string{"handoff", "lintfix", "cleanup"} {
		if got := r.count(phase); got != 1 {
			t.Errorf("%s ran %d times, want 1", phase, got)
		}
	}
	if got := p.State.Get(id, "PHASE"); got != state.HandedOff {
		t.Errorf("PHASE = %q, want %q after both sides finish", got, state.HandedOff)
	}
	for phase, want := range map[string]string{"handoff": "handoff-ok", "lintfix": "lintfix-ok", "cleanup": "cleanup-ok"} {
		data, err := os.ReadFile(filepath.Join(p.RunsDir, id, phase+".log"))
		if err != nil {
			t.Errorf("read %s transcript: %v", phase, err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s transcript = %q, want %q", phase, string(data), want)
		}
	}
}

// TestHandoffAndCleanupResumeSkipsHandoff covers the handed_off resume entry point:
// with runHandoff=false the handoff agent is not run and the checkpoint is left as it
// was, but the lintfix→cleanup chain still runs before verify.
func TestHandoffAndCleanupResumeSkipsHandoff(t *testing.T) {
	id := "COD-79801"
	cleanTmp(t, id)
	r := &overlapRunner{}
	p := newTestPipeline(t, r, &fakeTracker{})
	p.LintFix = true
	p.Cleanup = true
	if err := p.setPhase(id, state.HandedOff); err != nil {
		t.Fatal(err)
	}

	if err := p.handoffAndCleanup(context.Background(), id, false); err != nil {
		t.Fatalf("handoffAndCleanup: %v", err)
	}

	if got := r.count("handoff"); got != 0 {
		t.Errorf("handoff ran %d times on resume, want 0", got)
	}
	if got := r.count("lintfix"); got != 1 {
		t.Errorf("lintfix ran %d times, want 1", got)
	}
	if got := r.count("cleanup"); got != 1 {
		t.Errorf("cleanup ran %d times, want 1", got)
	}
	if got := p.State.Get(id, "PHASE"); got != state.HandedOff {
		t.Errorf("PHASE = %q, want it unchanged at %q", got, state.HandedOff)
	}
}

// TestHandoffAndCleanupTinyDiffSkipsCleanup guards the tiny-diff skip under the
// overlap: cleanup is dropped for a small working tree while handoff still runs
// alongside lintfix and the checkpoint still lands.
func TestHandoffAndCleanupTinyDiffSkipsCleanup(t *testing.T) {
	id := "COD-79802"
	cleanTmp(t, id)
	r := &overlapRunner{hooks: map[string]func(context.Context) (agent.Result, error){
		"handoff": writeBriefHook(id),
	}}
	p := newTestPipeline(t, r, &fakeTracker{})
	p.Git = sizeGit{files: 3, lines: 40}
	p.LintFix = true
	p.Cleanup = true

	if err := p.handoffAndCleanup(context.Background(), id, true); err != nil {
		t.Fatalf("handoffAndCleanup: %v", err)
	}

	if got := r.count("cleanup"); got != 0 {
		t.Errorf("cleanup ran %d times on a tiny diff, want 0", got)
	}
	if got := r.count("handoff"); got != 1 {
		t.Errorf("handoff ran %d times, want 1", got)
	}
	if got := r.count("lintfix"); got != 1 {
		t.Errorf("lintfix ran %d times, want 1", got)
	}
	if got := p.State.Get(id, "PHASE"); got != state.HandedOff {
		t.Errorf("PHASE = %q, want %q", got, state.HandedOff)
	}
}

// TestHandoffAndCleanupCleanupFailsOpen confirms cleanup's fail-open contract holds
// under the overlap: a non-fatal cleanup agent error is swallowed, both sides are
// considered complete, and the checkpoint lands.
func TestHandoffAndCleanupCleanupFailsOpen(t *testing.T) {
	id := "COD-79803"
	cleanTmp(t, id)
	r := &overlapRunner{hooks: map[string]func(context.Context) (agent.Result, error){
		"handoff": writeBriefHook(id),
		"cleanup": func(context.Context) (agent.Result, error) {
			return agent.Result{}, errors.New("cleanup agent crashed")
		},
	}}
	p := newTestPipeline(t, r, &fakeTracker{})
	p.LintFix = true
	p.Cleanup = true

	if err := p.handoffAndCleanup(context.Background(), id, true); err != nil {
		t.Fatalf("handoffAndCleanup should fail open on a non-fatal cleanup error, got %v", err)
	}
	if got := r.count("cleanup"); got != 1 {
		t.Errorf("cleanup ran %d times, want 1", got)
	}
	if got := p.State.Get(id, "PHASE"); got != state.HandedOff {
		t.Errorf("PHASE = %q, want %q", got, state.HandedOff)
	}
}

// TestHandoffAndCleanupFatalCancels checks that a fatal outcome on either side cancels
// the other, propagates the fatal error unchanged, and leaves the checkpoint unwritten
// (so a resume re-runs both sides from build).
func TestHandoffAndCleanupFatalCancels(t *testing.T) {
	rateLimit := func(phase string) error {
		return errors.New("kimi run (" + phase + "): 429 usage limit reached")
	}

	t.Run("handoff pause cancels the chain", func(t *testing.T) {
		id := "COD-79804"
		cleanTmp(t, id)
		r := &overlapRunner{}
		r.hooks = map[string]func(context.Context) (agent.Result, error){
			"handoff": func(context.Context) (agent.Result, error) {
				return agent.Result{}, rateLimit("handoff")
			},
			"lintfix": r.blockUntilCancelledHook("lintfix"),
		}
		p := newTestPipeline(t, r, &fakeTracker{})
		p.LintFix = true
		p.Cleanup = true

		err := p.handoffAndCleanup(context.Background(), id, true)
		if !IsPaused(err) {
			t.Fatalf("err = %v, want a *PausedError", err)
		}
		if !r.wasCancelled("lintfix") {
			t.Error("lintfix side was not cancelled when handoff paused")
		}
		if got := p.State.Get(id, "PHASE"); got == state.HandedOff {
			t.Error("checkpoint reached handed_off despite a fatal overlap outcome")
		}
	})

	t.Run("cleanup pause cancels handoff", func(t *testing.T) {
		id := "COD-79805"
		cleanTmp(t, id)
		r := &overlapRunner{}
		r.hooks = map[string]func(context.Context) (agent.Result, error){
			"handoff": r.blockUntilCancelledHook("handoff"),
			"cleanup": func(context.Context) (agent.Result, error) {
				return agent.Result{}, rateLimit("cleanup")
			},
		}
		p := newTestPipeline(t, r, &fakeTracker{})
		p.LintFix = true
		p.Cleanup = true

		err := p.handoffAndCleanup(context.Background(), id, true)
		if !IsPaused(err) {
			t.Fatalf("err = %v, want a *PausedError", err)
		}
		if !r.wasCancelled("handoff") {
			t.Error("handoff side was not cancelled when cleanup paused")
		}
		if got := p.State.Get(id, "PHASE"); got == state.HandedOff {
			t.Error("checkpoint reached handed_off despite a fatal overlap outcome")
		}
	})

	t.Run("plain handoff error propagates without a checkpoint", func(t *testing.T) {
		id := "COD-79806"
		cleanTmp(t, id)
		r := &overlapRunner{hooks: map[string]func(context.Context) (agent.Result, error){
			"handoff": func(context.Context) (agent.Result, error) {
				return agent.Result{}, errors.New("agent crashed")
			},
		}}
		p := newTestPipeline(t, r, &fakeTracker{})
		p.LintFix = true
		p.Cleanup = true

		err := p.handoffAndCleanup(context.Background(), id, true)
		if err == nil {
			t.Fatal("want the handoff error to propagate")
		}
		if IsPaused(err) {
			t.Fatalf("a plain crash must not be classified as a pause: %v", err)
		}
		if got := p.State.Get(id, "PHASE"); got == state.HandedOff {
			t.Error("checkpoint reached handed_off despite a failed handoff")
		}
	})
}
