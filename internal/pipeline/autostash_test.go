package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// stashGit is a fakeGit whose porcelain status and current branch are fixed per
// case, recording the stash / checkout / pop calls EnsureCleanBase and RestoreWIP
// make — plus the preserve-and-clean calls a reconcile makes — so the fresh-pick WIP
// guard can be exercised without a real repo.
type stashGit struct {
	fakeGit
	status      string
	branch      string
	stashErr    error
	checkoutErr error
	popErr      error

	stashed    []string
	checkouts  []string
	popped     int
	added      int
	commitMsgs []string
	pushes     int
	cleaned    int
}

func (g *stashGit) StatusPorcelain(context.Context) (string, error) { return g.status, nil }
func (g *stashGit) CurrentBranch(context.Context) (string, error)   { return g.branch, nil }
func (g *stashGit) Stash(_ context.Context, msg string) error {
	if g.stashErr != nil {
		return g.stashErr
	}
	g.stashed = append(g.stashed, msg)
	return nil
}
func (g *stashGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.checkouts = append(g.checkouts, ref)
	return g.checkoutErr
}
func (g *stashGit) StashPop(context.Context) error {
	if g.popErr != nil {
		return g.popErr
	}
	g.popped++
	return nil
}
func (g *stashGit) AddAll(context.Context) error { g.added++; return nil }
func (g *stashGit) Commit(_ context.Context, msg string, _ bool) error {
	g.commitMsgs = append(g.commitMsgs, msg)
	return nil
}
func (g *stashGit) Push(context.Context, string, string, bool) error { g.pushes++; return nil }
func (g *stashGit) Clean(context.Context) error                      { g.cleaned++; return nil }

// TestEnsureCleanBaseAutoStash covers the fresh-pick WIP guard: with AutoStash on,
// dirty tracked files are stashed (branch recorded) instead of aborting; with it
// off, or when the stash itself fails, the run aborts and no branch is recorded.
func TestEnsureCleanBaseAutoStash(t *testing.T) {
	cases := []struct {
		name        string
		autoStash   bool
		status      string
		stashErr    error
		wantErr     bool
		wantStashed bool
	}{
		{name: "dirty + autostash on → stash and proceed", autoStash: true, status: " M foo.go", wantStashed: true},
		{name: "dirty + autostash off → abort", autoStash: false, status: " M foo.go", wantErr: true},
		{name: "clean tree → nothing to stash", autoStash: true, status: "", wantStashed: false},
		{name: "dirty + stash fails → abort, WIP untouched", autoStash: true, status: " M foo.go", stashErr: errors.New("boom"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.AutoStash = tc.autoStash
			g := &stashGit{status: tc.status, branch: "feature/wip", stashErr: tc.stashErr}
			p.Git = g

			err := p.EnsureCleanBase(context.Background())

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an abort error, got nil")
				}
				if p.stashedBranch != "" {
					t.Errorf("stashedBranch recorded on an abort path: %q", p.stashedBranch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantStashed {
				if len(g.stashed) != 1 {
					t.Fatalf("expected exactly one stash, got %d", len(g.stashed))
				}
				if p.stashedBranch != "feature/wip" {
					t.Errorf("stashedBranch = %q, want feature/wip", p.stashedBranch)
				}
			} else {
				if len(g.stashed) != 0 {
					t.Errorf("expected no stash, got %v", g.stashed)
				}
				if p.stashedBranch != "" {
					t.Errorf("stashedBranch set unexpectedly: %q", p.stashedBranch)
				}
			}
			// A clean base always ends on the base branch.
			if n := len(g.checkouts); n == 0 || g.checkouts[n-1] != "main" {
				t.Errorf("expected a checkout of base 'main', got %v", g.checkouts)
			}
		})
	}
}

// TestRestoreWIP covers session-end restore: it pops the stash back onto the
// original branch, is a no-op when nothing was stashed, is idempotent (so a
// double-deferred call is safe), and leaves the WIP in the stash if it can't
// return to the branch.
func TestRestoreWIP(t *testing.T) {
	t.Run("no stash → no-op", func(t *testing.T) {
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		g := &stashGit{}
		p.Git = g
		p.RestoreWIP(context.Background())
		if len(g.checkouts) != 0 || g.popped != 0 {
			t.Errorf("expected a no-op; checkouts=%v popped=%d", g.checkouts, g.popped)
		}
	})

	t.Run("restores branch and pops, idempotent", func(t *testing.T) {
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		g := &stashGit{}
		p.Git = g
		p.stashedBranch = "feature/wip"

		p.RestoreWIP(context.Background())

		if len(g.checkouts) != 1 || g.checkouts[0] != "feature/wip" {
			t.Errorf("expected checkout of feature/wip, got %v", g.checkouts)
		}
		if g.popped != 1 {
			t.Errorf("expected one stash pop, got %d", g.popped)
		}
		if p.stashedBranch != "" {
			t.Errorf("stashedBranch not consumed: %q", p.stashedBranch)
		}

		p.RestoreWIP(context.Background())
		if len(g.checkouts) != 1 || g.popped != 1 {
			t.Errorf("second call was not a no-op; checkouts=%v popped=%d", g.checkouts, g.popped)
		}
	})

	t.Run("checkout fails → pop not attempted, WIP left in stash", func(t *testing.T) {
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		g := &stashGit{checkoutErr: errors.New("dirty tree")}
		p.Git = g
		p.stashedBranch = "feature/wip"

		p.RestoreWIP(context.Background())

		if g.popped != 0 {
			t.Error("stash pop attempted after a failed checkout")
		}
		if p.stashedBranch != "" {
			t.Errorf("stashedBranch should be consumed even on failure: %q", p.stashedBranch)
		}
	})
}

// TestEnsureCleanBaseReconcilesInterruptedRunWIP covers the crash leftover: a dirty
// tree parked on a branch trau cut for a checkpointed ticket is that run's work, so
// the next fresh pick commits it there — never stashing it, and regardless of
// AUTO_STASH. A branch trau never cut stays the user's WIP.
func TestEnsureCleanBaseReconcilesInterruptedRunWIP(t *testing.T) {
	const id = "COD-4242"
	cases := []struct {
		name         string
		branch       string
		recorded     string
		autoStash    bool
		wantPreserve bool
	}{
		{name: "branch trau cut for the checkpoint", branch: "feature/COD-4242-add-thing", autoStash: true, wantPreserve: true},
		{name: "reconciles with autostash off", branch: "feature/COD-4242-add-thing", wantPreserve: true},
		{name: "checkpoint recorded an off-pattern branch", branch: "wip/COD-4242", recorded: "wip/COD-4242", autoStash: true, wantPreserve: true},
		{name: "feature branch trau never cut is user WIP", branch: "feature/COD-9999-mine", autoStash: true},
		{name: "base branch is user WIP", branch: "main", autoStash: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.AutoStash = tc.autoStash
			p.Remote = "origin"
			g := &stashGit{status: " M internal/foo.go\nA  internal/bar.go", branch: tc.branch}
			p.Git = g
			if err := p.State.Set(id, "PHASE", state.Building); err != nil {
				t.Fatal(err)
			}
			if tc.recorded != "" {
				if err := p.State.Set(id, "BRANCH", tc.recorded); err != nil {
					t.Fatal(err)
				}
			}

			if err := p.EnsureCleanBase(context.Background()); err != nil {
				t.Fatalf("EnsureCleanBase err = %v", err)
			}

			if !tc.wantPreserve {
				if len(g.commitMsgs) != 0 {
					t.Fatalf("the user's WIP was committed as an interrupted run: %v", g.commitMsgs)
				}
				if len(g.stashed) != 1 || p.stashedBranch != tc.branch {
					t.Errorf("the user's WIP was not stashed: stashed=%v stashedBranch=%q", g.stashed, p.stashedBranch)
				}
				return
			}

			if len(g.commitMsgs) != 1 || !strings.Contains(g.commitMsgs[0], id) {
				t.Fatalf("commit msgs = %v, want one wip commit naming %s", g.commitMsgs, id)
			}
			if g.added == 0 || g.pushes == 0 || g.cleaned == 0 {
				t.Errorf("leftovers not fully reconciled: adds=%d pushes=%d cleans=%d", g.added, g.pushes, g.cleaned)
			}
			if len(g.stashed) != 0 || p.stashedBranch != "" {
				t.Fatalf("an interrupted run's WIP must never be stashed: stashed=%v stashedBranch=%q", g.stashed, p.stashedBranch)
			}
			if n := len(g.checkouts); n == 0 || g.checkouts[n-1] != "main" {
				t.Errorf("expected to end on base 'main', got %v", g.checkouts)
			}

			p.RestoreWIP(context.Background())
			if g.popped != 0 {
				t.Error("RestoreWIP popped a stash after a reconcile — it must be a no-op")
			}
		})
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestEnsureCleanBaseReconcilesAgainstRealGit exercises the same reconcile against a
// real repo: modified and newly-added files stranded on a checkpointed ticket's
// feature branch become a wip commit there, and the tree returns to a clean base.
func TestEnsureCleanBaseReconcilesAgainstRealGit(t *testing.T) {
	const id = "COD-4243"
	branch := "feature/" + id + "-orphan"

	work := t.TempDir()
	gitRun(t, work, "init")
	gitRun(t, work, "config", "user.name", "t")
	gitRun(t, work, "config", "user.email", "t@t")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "branch", "-M", "main")
	gitRun(t, work, "checkout", "-b", branch)
	writeRepoFile(t, work, "a.txt", "agent edit\n")
	writeRepoFile(t, work, "b.txt", "agent addition\n")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Git = ExecGit{Repo: work}
	p.Remote = "origin"
	p.AutoStash = true
	if err := p.State.Set(id, "PHASE", state.Building); err != nil {
		t.Fatal(err)
	}

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase err = %v", err)
	}
	p.RestoreWIP(context.Background())

	if got := gitOut(t, work, "status", "--porcelain"); got != "" {
		t.Errorf("working tree not clean after the reconcile: %q", got)
	}
	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("HEAD = %q, want the base branch", got)
	}
	if got := gitOut(t, work, "stash", "list"); got != "" {
		t.Errorf("stash list = %q, want empty — an interrupted run's WIP is committed, never stashed", got)
	}
	if got := gitOut(t, work, "log", "-1", "--format=%s", branch); !strings.Contains(got, "wip("+id+")") {
		t.Errorf("%s tip subject = %q, want a wip(%s) commit", branch, got, id)
	}
	if got := gitOut(t, work, "show", "--name-only", "--format=", branch); !strings.Contains(got, "a.txt") || !strings.Contains(got, "b.txt") {
		t.Errorf("preserved commit touches %q, want both the edited and the newly-added file", got)
	}
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
