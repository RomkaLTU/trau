package pipeline

import (
	"context"
	"testing"
)

// TestSuccessClearsFailureReason: a stale FAILURE_REASON from an earlier faulted
// attempt must not survive a run that ends successfully — neither via the
// already-merged short-circuit nor via a nil phase-chain result.
func TestSuccessClearsFailureReason(t *testing.T) {
	t.Run("already-merged short-circuit", func(t *testing.T) {
		id := "COD-90702"
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		mustSet(t, p, id, "FAILURE_REASON", "unexpected error during CI/merge: gh pr merge: exit status 1")
		mustSet(t, p, id, "PHASE", "merged")

		if err := p.Resume(context.Background(), id, "merged"); err != ErrAlreadyDone {
			t.Fatalf("Resume = %v, want ErrAlreadyDone", err)
		}
		if got := p.State.Get(id, "FAILURE_REASON"); got != "" {
			t.Errorf("FAILURE_REASON = %q after success, want cleared", got)
		}
	})

	t.Run("nil phase result", func(t *testing.T) {
		id := "COD-90703"
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		mustSet(t, p, id, "FAILURE_REASON", "unexpected error during verify: boom")

		if err := p.classifyPhaseErr(context.Background(), id, nil); err != nil {
			t.Fatalf("classifyPhaseErr(nil) = %v", err)
		}
		if got := p.State.Get(id, "FAILURE_REASON"); got != "" {
			t.Errorf("FAILURE_REASON = %q after success, want cleared", got)
		}
	})
}

func mustSet(t *testing.T, p *Pipeline, id, key, value string) {
	t.Helper()
	if err := p.State.Set(id, key, value); err != nil {
		t.Fatal(err)
	}
}
