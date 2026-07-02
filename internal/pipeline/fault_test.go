package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// recordingGit is a fakeGit parked on a feature branch that records the
// preserve-and-clean calls finalizeFault makes, so the test can assert the WIP is
// committed and pushed rather than left dangling.
type recordingGit struct {
	fakeGit
	branch      string
	addAll      int
	commitMsgs  []string
	pushes      int
	pushNoVerif int
	cleans      int
}

func (g *recordingGit) CurrentBranch(context.Context) (string, error) { return g.branch, nil }
func (g *recordingGit) AddAll(context.Context) error                  { g.addAll++; return nil }
func (g *recordingGit) Commit(_ context.Context, msg string, _ bool) error {
	g.commitMsgs = append(g.commitMsgs, msg)
	return nil
}
func (g *recordingGit) Push(_ context.Context, _, _ string, noVerify bool) error {
	g.pushes++
	if noVerify {
		g.pushNoVerif++
	}
	return nil
}
func (g *recordingGit) Clean(context.Context) error { g.cleans++; return nil }

// TestUnexpectedErrorFaultsBlamelessly is the COD-498 regression guard: an
// UNEXPECTED (non-rate-limit, non-give-up) agent error during build must NOT leave
// the ticket frozen with uncommitted WIP. It must preserve the work on the feature
// branch, return the tree to a clean base, leave the ticket resumable at its
// checkpoint, and report a *FaultError — without quarantining or filing a bug.
func TestUnexpectedErrorFaultsBlamelessly(t *testing.T) {
	id := "COD-700"
	tr := &fakeTracker{}
	git := &recordingGit{branch: "feature/COD-700-x"}
	boom := errors.New("kimi run (build): process exited unexpectedly")
	p := newTestPipeline(t, fakeRunner{err: boom}, tr)
	p.Git = git
	p.Remote = "origin"
	if err := p.State.Set(id, "BRANCH", git.branch); err != nil {
		t.Fatal(err)
	}

	err := p.Process(context.Background(), id)

	if !IsFault(err) {
		t.Fatalf("Process err = %v, want a *FaultError", err)
	}
	if IsPaused(err) {
		t.Fatalf("an unexpected error must not pause: %v", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("FaultError must wrap the original error, got %v", err)
	}
	var f *FaultError
	if errors.As(err, &f); f.Phase != state.Building {
		t.Errorf("fault phase = %q, want %q", f.Phase, state.Building)
	}

	if tr.fileBugCalls != 0 {
		t.Errorf("FileBug called %d times, want 0 (an infra fault is not a verified failure)", tr.fileBugCalls)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0 (the ticket must stay resumable)", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Building {
		t.Errorf("PHASE = %q, want it left at its checkpoint (%q)", got, state.Building)
	}

	if git.addAll == 0 || len(git.commitMsgs) != 1 {
		t.Fatalf("WIP not committed: addAll=%d commits=%v", git.addAll, git.commitMsgs)
	}
	if msg := git.commitMsgs[0]; !strings.Contains(msg, id) || !strings.Contains(msg, "incomplete") {
		t.Errorf("commit msg = %q, want it to mention %s and 'incomplete'", msg, id)
	}
	if git.pushes == 0 {
		t.Error("expected the preserved branch to be pushed best-effort")
	}
	if git.pushNoVerif != git.pushes {
		t.Errorf("WIP-preservation push must bypass hooks (--no-verify): %d/%d pushes did", git.pushNoVerif, git.pushes)
	}
	if git.cleans == 0 {
		t.Error("expected the working tree to be cleaned back to base")
	}
}
