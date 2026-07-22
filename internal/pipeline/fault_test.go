package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/state"
)

// recordingGit is a fakeGit parked on a feature branch that logs the
// preserve-and-clean calls the finalizers make — in order, and flagging any that
// arrived on a dead context — so tests can assert the WIP is committed and pushed
// rather than left dangling.
type recordingGit struct {
	fakeGit
	branch      string
	calls       []string
	deadCtx     []string
	commitMsgs  []string
	pushNoVerif int
}

func (g *recordingGit) record(ctx context.Context, call string) {
	g.calls = append(g.calls, call)
	if ctx.Err() != nil {
		g.deadCtx = append(g.deadCtx, call)
	}
}

func (g *recordingGit) count(call string) int {
	n := 0
	for _, c := range g.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (g *recordingGit) CurrentBranch(ctx context.Context) (string, error) {
	g.record(ctx, "current-branch")
	return g.branch, nil
}

func (g *recordingGit) AddAll(ctx context.Context) error {
	g.record(ctx, "add")
	return nil
}

func (g *recordingGit) Commit(ctx context.Context, msg string, _ bool) error {
	g.record(ctx, "commit")
	g.commitMsgs = append(g.commitMsgs, msg)
	return nil
}

func (g *recordingGit) Push(ctx context.Context, _, _ string, noVerify bool) error {
	g.record(ctx, "push")
	if noVerify {
		g.pushNoVerif++
	}
	return nil
}

func (g *recordingGit) Checkout(ctx context.Context, ref string, _ bool) error {
	g.record(ctx, "checkout "+ref)
	return nil
}

func (g *recordingGit) Clean(ctx context.Context) error {
	g.record(ctx, "clean")
	return nil
}

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
	if got := p.State.Get(id, "FAILURE_CLASS"); got != state.FailFaulted {
		t.Errorf("FAILURE_CLASS = %q, want %q so a file-first reader flags the fault", got, state.FailFaulted)
	}

	if git.count("add") == 0 || len(git.commitMsgs) != 1 {
		t.Fatalf("WIP not committed: adds=%d commits=%v", git.count("add"), git.commitMsgs)
	}
	if msg := git.commitMsgs[0]; !strings.Contains(msg, id) || !strings.Contains(msg, "incomplete") {
		t.Errorf("commit msg = %q, want it to mention %s and 'incomplete'", msg, id)
	}
	pushes := git.count("push")
	if pushes == 0 {
		t.Error("expected the preserved branch to be pushed best-effort")
	}
	if git.pushNoVerif != pushes {
		t.Errorf("WIP-preservation push must bypass hooks (--no-verify): %d/%d pushes did", git.pushNoVerif, pushes)
	}
	if git.count("clean") == 0 {
		t.Error("expected the working tree to be cleaned back to base")
	}
}

// TestFinalizeRunsDetachedFromACancelledRun guards the stop path: stopping a run
// cancels the pipeline context, and the preserve-and-clean that follows is exactly
// the work that must still happen. On the run's own context every git step fails
// instantly, leaving the repo dirty on the feature branch.
func TestFinalizeRunsDetachedFromACancelledRun(t *testing.T) {
	id := "COD-1092"
	want := []string{"current-branch", "add", "commit", "push", "checkout main", "clean"}
	finalizers := map[string]func(*Pipeline, context.Context, string){
		"fault":  (*Pipeline).finalizeFault,
		"failed": (*Pipeline).finalizeFailed,
	}

	for name, finalize := range finalizers {
		t.Run(name, func(t *testing.T) {
			git := &recordingGit{branch: "feature/COD-1092-x"}
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = git
			p.Remote = "origin"

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			finalize(p, ctx, id)

			if !slices.Equal(git.calls, want) {
				t.Fatalf("git calls = %v, want %v", git.calls, want)
			}
			if len(git.deadCtx) != 0 {
				t.Errorf("%v ran on the cancelled run context, so git would abort them — the cleanup must be detached", git.deadCtx)
			}
			if len(git.commitMsgs) != 1 || !strings.Contains(git.commitMsgs[0], id) {
				t.Errorf("commit msgs = %v, want one mentioning %s", git.commitMsgs, id)
			}
		})
	}
}

// TestExecGitRunHonoursItsDeadline makes cleanupPushBudget a real bound rather
// than a paper one. A push against an unreachable remote leaves an ssh grandchild
// holding git's output pipe well after the deadline kills git itself, and the
// stand-in below reproduces that: the run must still return, or the checkout and
// clean that follow it inherit an already-expired cleanup context.
func TestExecGitRunHonoursItsDeadline(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "git")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := ExecGit{Bin: bin, Repo: dir}.run(ctx, "push", "-u", "origin", "HEAD")
	elapsed := time.Since(start)

	if err == nil {
		t.Error("a push killed by its deadline must report an error so the caller falls back to the local branch")
	}
	if want := gitWaitDelay + 5*time.Second; elapsed > want {
		t.Fatalf("run took %v, want under %v — a grandchild holding the output pipe must not outlast the deadline", elapsed, want)
	}
}
