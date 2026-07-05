package pipeline

import (
	"context"
	"errors"
	"testing"
)

// stashGit is a fakeGit whose porcelain status and current branch are fixed per
// case, recording the stash / checkout / pop calls EnsureCleanBase and RestoreWIP
// make so the auto-stash guard can be exercised without a real repo.
type stashGit struct {
	fakeGit
	status      string
	branch      string
	stashErr    error
	checkoutErr error
	popErr      error

	stashed   []string
	checkouts []string
	popped    int
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
