package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/state"
)

func TestResumeInstallsTheTicketsSelectedRunner(t *testing.T) {
	pinned := fakeRunner{err: errors.New("stop here")}
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	asked := ""
	p.SelectRunner = func(_ context.Context, id string) (agent.Runner, string, error) {
		asked = id
		return pinned, "codex", nil
	}

	_ = p.Resume(context.Background(), "COD-1", "")

	if asked != "COD-1" {
		t.Fatalf("resolver asked for %q, want COD-1", asked)
	}
	if p.Runner != agent.Runner(pinned) {
		t.Fatalf("Runner = %v, want the ticket's selected backend installed", p.Runner)
	}
}

func TestResumePausesWhenThePinnedProviderCannotBeBuilt(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.SelectRunner = func(context.Context, string) (agent.Runner, string, error) {
		return nil, "codex", errors.New(`provider "codex": "codex" not found on PATH`)
	}

	err := p.Resume(context.Background(), "COD-1", "")

	pe := AsPaused(err)
	if pe == nil {
		t.Fatalf("Resume err = %v, want a *PausedError", err)
	}
	if pe.Provider != "codex" {
		t.Fatalf("pause provider = %q, want the pinned codex named", pe.Provider)
	}
	if !strings.Contains(pe.Reason, "not found on PATH") {
		t.Fatalf("pause reason = %q, want it to carry the build failure", pe.Reason)
	}
	if got := p.State.Get("COD-1", "FAILURE_CLASS"); got != state.FailPaused {
		t.Fatalf("FAILURE_CLASS = %q, want %q", got, state.FailPaused)
	}
	if got := p.State.Get("COD-1", "PHASE"); got != "" {
		t.Fatalf("PHASE = %q, want the ticket untouched — nothing ran", got)
	}
}
