package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// worktreeRefusal is git's verbatim complaint when a sibling worktree already has
// the base branch checked out — the message the string fallback keys on.
const worktreeRefusal = "git checkout main: exit status 128: fatal: 'main' is already used by worktree at '/Users/rd/Projects/loop-fix'"

// worktreeGit refuses checkouts of the base branch the way git does when another
// worktree holds it, recording what the fallback reached for instead.
type worktreeGit struct {
	fakeGit
	branch   string
	status   string
	holder   string
	holdErr  error
	refusal  error
	fetchErr error

	checkouts []string
	detached  []detachCall
	fetches   []string
	cutFrom   string
	pulls     int
	commits   int
	pushes    int
	cleaned   int
}

// detachCall is one CheckoutDetached, kept with its force flag so the sweep-back
// paths can be held to discarding a killed run's WIP the way an attached checkout does.
type detachCall struct {
	ref   string
	force bool
}

func (g *worktreeGit) CurrentBranch(context.Context) (string, error)   { return g.branch, nil }
func (g *worktreeGit) StatusPorcelain(context.Context) (string, error) { return g.status, nil }

func (g *worktreeGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.checkouts = append(g.checkouts, ref)
	if ref != "main" {
		return nil
	}
	if g.refusal != nil {
		return g.refusal
	}
	return errors.New(worktreeRefusal)
}

func (g *worktreeGit) CheckoutDetached(_ context.Context, ref string, force bool) error {
	g.detached = append(g.detached, detachCall{ref: ref, force: force})
	return nil
}

func (g *worktreeGit) WorktreeHolding(context.Context, string) (string, error) {
	return g.holder, g.holdErr
}

func (g *worktreeGit) CreateBranch(_ context.Context, _, base string) error {
	g.cutFrom = base
	return nil
}

func (g *worktreeGit) Fetch(_ context.Context, remote, branch string) error {
	g.fetches = append(g.fetches, remote+"/"+branch)
	return g.fetchErr
}

func (g *worktreeGit) Pull(context.Context, string, string) error       { g.pulls++; return nil }
func (g *worktreeGit) Commit(context.Context, string, bool) error       { g.commits++; return nil }
func (g *worktreeGit) Push(context.Context, string, string, bool) error { g.pushes++; return nil }
func (g *worktreeGit) Clean(context.Context) error                      { g.cleaned++; return nil }

func newWorktreePipeline(t *testing.T, g *worktreeGit) *Pipeline {
	t.Helper()
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Remote = "origin"
	p.AutoStash = true
	p.Git = g
	return p
}

// An operator's sibling worktree sitting on the base branch makes git refuse the
// fresh-pick checkout, and the run must ride the base tip detached instead of pausing
// the whole queue. A checkout that failed for any other reason still aborts.
func TestEnsureCleanBaseDetachesWhenWorktreeHoldsBase(t *testing.T) {
	cases := []struct {
		name       string
		holder     string
		holdErr    error
		refusal    error
		fetchErr   error
		wantDetach string
		wantErr    bool
	}{
		{
			name:       "worktree list names the holder",
			holder:     "/Users/rd/Projects/loop-fix",
			wantDetach: "origin/main",
		},
		{
			name:       "unreachable remote falls back to the local base tip",
			holder:     "/Users/rd/Projects/loop-fix",
			fetchErr:   errors.New("could not read from remote repository"),
			wantDetach: "main",
		},
		{
			name:       "worktree list unavailable falls back to git's refusal message",
			holdErr:    errors.New("git worktree list: exit status 128"),
			wantDetach: "origin/main",
		},
		{
			name:    "unrelated checkout failure aborts",
			refusal: errors.New("git checkout main: exit status 1: error: pathspec 'main' did not match"),
			wantErr: true,
		},
		{
			name:    "unrelated failure with no worktree answer aborts",
			refusal: errors.New("git checkout main: exit status 1"),
			holdErr: errors.New("git worktree list: exit status 128"),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &worktreeGit{
				branch:   "main",
				holder:   tc.holder,
				holdErr:  tc.holdErr,
				refusal:  tc.refusal,
				fetchErr: tc.fetchErr,
			}
			p := newWorktreePipeline(t, g)

			err := p.EnsureCleanBase(context.Background())

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected the checkout failure to abort, got nil")
				}
				if len(g.detached) != 0 {
					t.Errorf("detached HEAD on a failure that was not a worktree conflict: %v", g.detached)
				}
				if got := p.baseRef(); got != p.Base {
					t.Errorf("baseRef = %q, want the base branch when nothing was detached", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("EnsureCleanBase err = %v, want the worktree conflict auto-resolved", err)
			}
			if len(g.detached) != 1 || g.detached[0].ref != tc.wantDetach {
				t.Fatalf("detached at %v, want [%s]", g.detached, tc.wantDetach)
			}
			if got := p.baseRef(); got != tc.wantDetach {
				t.Errorf("baseRef = %q, want branches cut from the tip HEAD was parked at (%s)", got, tc.wantDetach)
			}
			if g.pulls != 0 {
				t.Errorf("pulled %d times while detached — the fetched tip is already current", g.pulls)
			}
		})
	}
}

// A detached HEAD is the base, not a run's branch, so nothing on it is adopted as an
// in-progress ticket or committed back as an interrupted run's leftovers.
func TestDetachedBaseIsNotAdoptedAsInProgressWork(t *testing.T) {
	const id = "COD-1104"

	t.Run("detached HEAD is never inferred as a resume", func(t *testing.T) {
		p := newWorktreePipeline(t, &worktreeGit{branch: detachedHead})

		gotID, gotPhase := p.InferredResume(context.Background())

		if gotID != "" || gotPhase != "" {
			t.Errorf("InferredResume = (%q, %q), want no adoption of a detached HEAD", gotID, gotPhase)
		}
	})

	t.Run("WIP on a detached base is the user's, not an interrupted run's", func(t *testing.T) {
		g := &worktreeGit{branch: detachedHead, status: " M internal/foo.go", holder: "/Users/rd/Projects/loop-fix"}
		p := newWorktreePipeline(t, g)
		if err := p.State.Set(id, "PHASE", state.Building); err != nil {
			t.Fatal(err)
		}

		if err := p.EnsureCleanBase(context.Background()); err != nil {
			t.Fatalf("EnsureCleanBase err = %v", err)
		}

		if g.commits != 0 || g.pushes != 0 {
			t.Errorf("a detached base was preserved as run WIP: commits=%d pushes=%d", g.commits, g.pushes)
		}
		if p.stashedBranch != detachedHead {
			t.Errorf("stashedBranch = %q, want the detached head", p.stashedBranch)
		}
	})
}

// An epic branch is cut for a whole chain of slices, so it must start at the tip HEAD
// was parked at too — the local base the sibling worktree pins is the stale one.
func TestEpicBranchCutFromTheDetachedBaseTip(t *testing.T) {
	g := &worktreeGit{branch: "main", holder: "/Users/rd/Projects/loop-fix"}
	p := newWorktreePipeline(t, g)
	p.EpicID = "COD-1100"

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase err = %v", err)
	}
	if _, err := p.epicBranchName(context.Background()); err != nil {
		t.Fatalf("epicBranchName err = %v", err)
	}

	if g.cutFrom != "origin/main" {
		t.Errorf("epic branch cut from %q, want the fetched base tip origin/main", g.cutFrom)
	}
}

// The best-effort sweeps that run after a ticket ends must park the repo at the base
// tip rather than silently leaving the finished feature branch checked out, and they
// carry the same force an attached checkout would: reset and purge exist to throw a
// killed run's WIP away, so an uncommitted file must not strand HEAD on that branch.
func TestSweepBackDetachesWhenWorktreeHoldsBase(t *testing.T) {
	const id = "COD-1104"

	cases := []struct {
		name  string
		sweep func(*testing.T, *Pipeline)
	}{
		{
			name: "preserve-and-clean saves the branch then parks at the base tip",
			sweep: func(t *testing.T, p *Pipeline) {
				p.preserveAndClean(context.Background(), "wip("+id+"): stopped mid-run")
			},
		},
		{
			name:  "reset parks at the base tip",
			sweep: func(t *testing.T, p *Pipeline) { p.resetLocal(context.Background(), id) },
		},
		{
			name: "purge parks at the base tip",
			sweep: func(t *testing.T, p *Pipeline) {
				if err := p.PurgeLocal(context.Background(), id); err != nil {
					t.Fatalf("PurgeLocal err = %v", err)
				}
			},
		},
	}
	want := detachCall{ref: "origin/main", force: true}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := &worktreeGit{branch: "feature/" + id + "-thing", holder: "/Users/rd/Projects/loop-fix"}
			p := newWorktreePipeline(t, g)

			tc.sweep(t, p)

			if len(g.detached) != 1 || g.detached[0] != want {
				t.Fatalf("detached %v, want [%+v]", g.detached, want)
			}
		})
	}

	t.Run("preserve-and-clean commits the branch before parking", func(t *testing.T) {
		g := &worktreeGit{branch: "feature/" + id + "-thing", holder: "/Users/rd/Projects/loop-fix"}
		p := newWorktreePipeline(t, g)

		p.preserveAndClean(context.Background(), "wip("+id+"): stopped mid-run")

		if g.commits != 1 {
			t.Errorf("commits = %d, want the feature branch's WIP preserved", g.commits)
		}
		if g.cleaned != 1 {
			t.Errorf("cleans = %d, want the tree cleaned after the sweep", g.cleaned)
		}
	})
}

// worktreeHeldRepo builds the shape this slice exists for: a clone whose sibling
// worktree holds main, with origin's main one commit ahead of the clone's, so a base
// checkout is refused AND the local base is observably behind the tip it fetches.
func worktreeHeldRepo(t *testing.T) (work, sibling, originHead string) {
	t.Helper()

	origin := t.TempDir()
	gitRun(t, origin, "init", "-b", "main")
	writeRepoFile(t, origin, "a.txt", "base\n")
	gitRun(t, origin, "add", "-A")
	gitRun(t, origin, "commit", "-m", "init")

	work = t.TempDir()
	gitRun(t, work, "clone", origin, work)
	gitRun(t, work, "checkout", "-b", "scratch")

	sibling = t.TempDir()
	gitRun(t, work, "worktree", "add", sibling, "main")

	writeRepoFile(t, origin, "a.txt", "advanced\n")
	gitRun(t, origin, "commit", "-am", "advance main")
	return work, sibling, gitOut(t, origin, "rev-parse", "HEAD")
}

func newRealGitPipeline(t *testing.T, work string) *Pipeline {
	t.Helper()
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Git = ExecGit{Repo: work}
	p.Remote = "origin"
	p.AutoStash = true
	return p
}

// The whole auto-resolve against real git: a sibling worktree holds main, the run
// picks up detached at the up-to-date base tip, cuts its feature branch from those
// same commits and sweeps back to them, and the operator's worktree is left as it was.
func TestWorktreeHeldBaseAgainstRealGit(t *testing.T) {
	const id = "COD-1104"

	work, sibling, originHead := worktreeHeldRepo(t)
	siblingHead := gitOut(t, sibling, "rev-parse", "HEAD")

	holder, err := ExecGit{Repo: work}.WorktreeHolding(context.Background(), "main")
	if err != nil {
		t.Fatalf("WorktreeHolding err = %v", err)
	}
	if holder == "" {
		t.Fatal("worktree list did not name the worktree holding main")
	}
	own, err := ExecGit{Repo: work}.WorktreeHolding(context.Background(), "scratch")
	if err != nil {
		t.Fatalf("WorktreeHolding err = %v", err)
	}
	if own != "" {
		t.Errorf("our own checkout of scratch was reported as a conflict at %s", own)
	}

	p := newRealGitPipeline(t, work)

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase err = %v, want the worktree conflict auto-resolved", err)
	}

	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != detachedHead {
		t.Errorf("HEAD = %q, want a detached HEAD", got)
	}
	if got := gitOut(t, work, "rev-parse", "HEAD"); got != originHead {
		t.Errorf("HEAD is at %s, want the fetched base tip %s", got, originHead)
	}

	branch, err := p.resolveBuildBranch(context.Background(), id)
	if err != nil {
		t.Fatalf("resolveBuildBranch err = %v", err)
	}
	if got := gitOut(t, work, "rev-parse", branch); got != originHead {
		t.Errorf("%s was cut at %s, want the fetched base tip %s HEAD was parked at", branch, got, originHead)
	}
	upstream := gitOut(t, work, "for-each-ref", "--format=%(upstream)", "refs/heads/"+branch)
	if upstream != "" {
		t.Errorf("%s tracks %s — a bare push on a run's branch would target the base", branch, upstream)
	}
	writeRepoFile(t, work, "b.txt", "agent work\n")

	p.preserveAndClean(context.Background(), "wip("+id+"): stopped mid-run")

	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != detachedHead {
		t.Errorf("after the sweep HEAD = %q, want to be parked at the base tip detached", got)
	}
	if got := gitOut(t, work, "log", "-1", "--format=%s", branch); !strings.Contains(got, "wip("+id+")") {
		t.Errorf("%s tip subject = %q, want the attempt preserved there", branch, got)
	}
	if got := gitOut(t, work, "status", "--porcelain"); got != "" {
		t.Errorf("working tree not clean after the sweep: %q", got)
	}

	if got := gitOut(t, sibling, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("sibling worktree HEAD = %q, want it left on main", got)
	}
	if got := gitOut(t, sibling, "rev-parse", "HEAD"); got != siblingHead {
		t.Errorf("sibling worktree moved to %s, want it untouched at %s", got, siblingHead)
	}
	if got := gitOut(t, sibling, "status", "--porcelain"); got != "" {
		t.Errorf("sibling worktree tree dirtied: %q", got)
	}
}

// Reset and hard-delete exist to throw a killed run's WIP away, so against real git an
// uncommitted tracked file must not strand them on the feature branch: both park HEAD
// at the base tip the sibling worktree denies them and drop the branch behind them.
func TestSweepBackDiscardsWIPAgainstRealGit(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		sweep func(*testing.T, *Pipeline, string)
	}{
		{
			name:  "reset",
			id:    "COD-1104",
			sweep: func(_ *testing.T, p *Pipeline, id string) { p.resetLocal(context.Background(), id) },
		},
		{
			name: "hard delete",
			id:   "COD-1105",
			sweep: func(t *testing.T, p *Pipeline, id string) {
				if err := p.PurgeLocal(context.Background(), id); err != nil {
					t.Fatalf("PurgeLocal err = %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, _, originHead := worktreeHeldRepo(t)
			p := newRealGitPipeline(t, work)

			if err := p.EnsureCleanBase(context.Background()); err != nil {
				t.Fatalf("EnsureCleanBase err = %v", err)
			}
			branch, err := p.resolveBuildBranch(context.Background(), tc.id)
			if err != nil {
				t.Fatalf("resolveBuildBranch err = %v", err)
			}
			if err := p.State.Set(tc.id, "BRANCH", branch); err != nil {
				t.Fatal(err)
			}
			writeRepoFile(t, work, "a.txt", "half-finished agent work\n")

			tc.sweep(t, p, tc.id)

			if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != detachedHead {
				t.Errorf("HEAD = %q, want the sweep parked at the base tip detached", got)
			}
			if got := gitOut(t, work, "rev-parse", "HEAD"); got != originHead {
				t.Errorf("HEAD is at %s, want the base tip %s", got, originHead)
			}
			if exists, _ := p.Git.BranchExists(context.Background(), branch); exists {
				t.Errorf("%s survived the sweep", branch)
			}
			if got := gitOut(t, work, "status", "--porcelain"); got != "" {
				t.Errorf("the discarded WIP is still in the tree: %q", got)
			}
		})
	}
}
