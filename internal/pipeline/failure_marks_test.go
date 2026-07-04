package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestRateLimitPausePersistsMarker guards the durable pause contract the file-
// first runs surface reads: a blameless rate-limit pause must record a paused
// marker and reason on the checkpoint while leaving it in-flight, so trau serve
// can tell a pause apart from a fault after the loop has stopped.
func TestRateLimitPausePersistsMarker(t *testing.T) {
	id := "COD-90583"
	writeHandoff(t, id)
	p := newTestPipeline(t, fakeRunner{err: errors.New("kimi run (verify): 429 usage limit reached")}, &fakeTracker{})

	err := p.Verify(context.Background(), id)

	if !IsPaused(err) {
		t.Fatalf("Verify err = %v, want a *PausedError", err)
	}
	if got := p.State.Get(id, "FAILURE_CLASS"); got != state.FailPaused {
		t.Errorf("FAILURE_CLASS = %q, want %q", got, state.FailPaused)
	}
	if got := p.State.Get(id, "FAILURE_REASON"); got == "" {
		t.Errorf("FAILURE_REASON empty, want the pause reason recorded")
	}
	if state.Terminal(p.State.Get(id, "PHASE")) {
		t.Errorf("PHASE = %q is terminal, want the ticket left resumable", p.State.Get(id, "PHASE"))
	}
}

// TestClearFailureMarks covers the retry side: a prior attempt's markers are
// dropped as the ticket runs again, while a fresh ticket keeps its first
// checkpoint being the build phase rather than an empty state file.
func TestClearFailureMarks(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})

	p.clearFailureMarks("COD-1")
	if len(p.State.Tickets()) != 0 {
		t.Errorf("clearing a fresh ticket created a state file: %v", p.State.Tickets())
	}

	id := "COD-2"
	_ = p.State.Set(id, "PHASE", state.Built)
	_ = p.State.Set(id, "FAILURE_CLASS", state.FailPaused)
	_ = p.State.Set(id, "FAILURE_REASON", "claude rate/usage limit reached")

	p.clearFailureMarks(id)

	if got := p.State.Get(id, "FAILURE_CLASS"); got != "" {
		t.Errorf("FAILURE_CLASS = %q, want cleared", got)
	}
	if got := p.State.Get(id, "FAILURE_REASON"); got != "" {
		t.Errorf("FAILURE_REASON = %q, want cleared", got)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Built {
		t.Errorf("PHASE = %q, want the checkpoint preserved (%q)", got, state.Built)
	}
}
