package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestClassifyHubUnreachablePauses guards the ADR 0008 §3 contract: a checkpoint
// write that fails because the hub stayed unreachable surfaces as a blameless
// pause — the same class as an auth-stall pause — not a fault. The ticket keeps
// its last phase and records a paused marker so the run resumes cleanly.
func TestClassifyHubUnreachablePauses(t *testing.T) {
	p := newTestPipeline(t, nil, nil)
	id := "COD-1"
	if err := p.State.Set(id, "PHASE", state.Verified); err != nil {
		t.Fatalf("seed phase: %v", err)
	}

	err := p.classifyPhaseErr(context.Background(), id, fmt.Errorf("checkpoint write: %w", state.ErrHubUnreachable))

	if !IsPaused(err) {
		t.Fatalf("classifyPhaseErr(ErrHubUnreachable) = %v; want a pause", err)
	}
	if pe := AsPaused(err); pe == nil || pe.Provider != "hub" {
		t.Fatalf("paused error = %+v; want provider hub", pe)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Verified {
		t.Fatalf("PHASE = %q after pause; want it left at verified", got)
	}
	if got := p.State.Get(id, "FAILURE_CLASS"); got != state.FailPaused {
		t.Fatalf("FAILURE_CLASS = %q; want paused", got)
	}
}
