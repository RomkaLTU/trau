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

// exitGit is a fakeGit that models the branches a run leaves behind: which ones
// exist, how many commits each carries over the base, and where HEAD sits — enough
// for ExitCleanup to be driven through every branch it has to settle.
type exitGit struct {
	fakeGit
	branch      string
	epicFound   string
	commits     map[string][]string
	commitsErr  error
	checkoutErr error

	checkouts     []string
	pushes        []string
	deleted       []string
	deletedRemote []string
}

func (g *exitGit) CurrentBranch(context.Context) (string, error) { return g.branch, nil }
func (g *exitGit) RemoteSHA(_ context.Context, _, branch string) (string, error) {
	return currentBaseSHA(branch), nil
}
func (g *exitGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.checkouts = append(g.checkouts, ref)
	if g.checkoutErr != nil {
		return g.checkoutErr
	}
	g.branch = ref
	return nil
}
func (g *exitGit) FindEpicBranch(context.Context, string) (string, error) {
	return g.epicFound, nil
}
func (g *exitGit) Commits(_ context.Context, base, branch string) ([]string, error) {
	return g.commits[base+".."+branch], g.commitsErr
}
func (g *exitGit) Push(_ context.Context, _, ref string, _ bool) error {
	g.pushes = append(g.pushes, ref)
	return nil
}
func (g *exitGit) DeleteBranch(_ context.Context, branch string) error {
	g.deleted = append(g.deleted, branch)
	return nil
}
func (g *exitGit) DeletePushedBranch(_ context.Context, _, branch string) error {
	g.deletedRemote = append(g.deletedRemote, branch)
	return nil
}

// TestExitCleanupRestoresStartBranch covers the checkout half of exit hygiene: the
// branch HEAD was on when the run started is checked back out however the run ends,
// including when the loop's context is already cancelled and including a resume that
// started on the ticket branch trau itself cut. A run that never left its branch does
// not churn git for a checkout it already has.
func TestExitCleanupRestoresStartBranch(t *testing.T) {
	const id = "COD-7100"
	cases := []struct {
		name         string
		start        string
		checkpointed bool
		cancelled    bool
		end          string
		want         string
	}{
		{name: "back from the epic branch", start: "main", end: "epic/COD-7100-thing", want: "main"},
		{name: "back onto a personal branch", start: "feature/mine", end: "main", want: "feature/mine"},
		{name: "interrupted run still restores", start: "main", end: "epic/COD-7100-thing", cancelled: true, want: "main"},
		{name: "already home, nothing to do", start: "main", end: "main", want: ""},
		{name: "a resume returns to the ticket branch it started on", start: "feature/" + id + "-x", checkpointed: true, end: "epic/COD-7100-thing", want: "feature/" + id + "-x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			g := &exitGit{branch: tc.start}
			p.Git = g
			if tc.checkpointed {
				if err := p.State.Set(id, "PHASE", state.Building); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.armExitCleanup(ctx)
			g.branch = tc.end
			if tc.cancelled {
				cancel()
			}

			p.ExitCleanup(ctx)

			if tc.want == "" {
				if len(g.checkouts) != 0 {
					t.Fatalf("expected no checkout, got %v", g.checkouts)
				}
				return
			}
			if len(g.checkouts) != 1 || g.checkouts[0] != tc.want {
				t.Fatalf("checkouts = %v, want a single checkout of %q", g.checkouts, tc.want)
			}
		})
	}
}

// TestSettleEpicBranch covers the epic half: a branch children landed work on is
// pushed and tracked by a draft PR, one nothing landed on is deleted — its remote
// copy only when this run pushed it there, and not at all when this run stacked work
// on it — and an epic this run shipped is left alone. A branch an earlier run left
// behind is settled even though this run never resolved it itself.
func TestSettleEpicBranch(t *testing.T) {
	const (
		id   = "COD-7101"
		epic = "epic/COD-7101-thing"
	)
	cases := []struct {
		name       string
		cached     string
		pushed     bool
		stacked    bool
		found      string
		commits    []string
		merged     bool
		commitsErr error

		wantPushed  bool
		wantPR      bool
		wantDeleted bool
		wantRemote  bool
	}{
		{name: "commits and no PR → pushed with a draft PR", cached: epic, pushed: true, commits: []string{"abc123"}, wantPushed: true, wantPR: true},
		{name: "no commits → the branch this run pushed is dropped everywhere", cached: epic, pushed: true, wantDeleted: true, wantRemote: true},
		{name: "no commits → a branch this run does not own keeps its remote copy", cached: epic, wantDeleted: true},
		{name: "no commits but work stacked on it → kept", cached: epic, pushed: true, stacked: true},
		{name: "an earlier run's branch is settled too", found: epic, commits: []string{"abc123"}, wantPushed: true, wantPR: true},
		{name: "a shipped epic has nothing left to settle", cached: epic, pushed: true, commits: []string{"abc123"}, merged: true},
		{name: "no epic branch anywhere", pushed: true},
		{name: "an unreadable branch is left in place", cached: epic, pushed: true, commitsErr: errors.New("bad object")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := &epicGitHub{createURL: "https://github.test/pr/7"}
			p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
			p.GitHub = gh
			p.Remote = "origin"
			p.EpicID = id
			p.exit.epicBranch, p.exit.epicPushed, p.exit.epicStacked = tc.cached, tc.pushed, tc.stacked
			g := &exitGit{
				branch:     "main",
				epicFound:  tc.found,
				commits:    map[string][]string{"origin/main.." + epic: tc.commits},
				commitsErr: tc.commitsErr,
			}
			p.Git = g
			if tc.merged {
				if err := p.State.Set(id, "PHASE", state.Merged); err != nil {
					t.Fatal(err)
				}
			}

			p.settleEpicBranch(context.Background())

			if got := len(g.pushes) > 0; got != tc.wantPushed {
				t.Errorf("pushed = %v, want %v (pushes: %v)", got, tc.wantPushed, g.pushes)
			}
			if got := gh.createCalls > 0; got != tc.wantPR {
				t.Errorf("PR created = %v, want %v", got, tc.wantPR)
			}
			if tc.wantPR && !gh.createDraft {
				t.Error("the PR exit hygiene opens for an unfinished epic must be a draft")
			}
			if got := len(g.deleted) > 0; got != tc.wantDeleted {
				t.Errorf("deleted = %v, want %v (deleted: %v)", got, tc.wantDeleted, g.deleted)
			}
			if got := len(g.deletedRemote) > 0; got != tc.wantRemote {
				t.Errorf("remote deleted = %v, want %v", got, tc.wantRemote)
			}
			if p.exit.epicBranch != "" || p.exit.epicPushed || p.exit.epicStacked {
				t.Errorf("the settled branch was not consumed: %+v", p.exit)
			}
		})
	}
}

// An epic branch is cut from the base as the remote has it, so its own work is
// counted there too: commits a local base has merely drifted behind must not read as
// work the branch carries, or an empty epic branch gets pushed and PR'd instead of
// dropped.
func TestSettleEpicBranchCountsWorkAgainstTheRemoteBase(t *testing.T) {
	const epic = "epic/COD-7108-thing"
	gh := &epicGitHub{createURL: "https://github.test/pr/13"}
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
	p.GitHub = gh
	p.Remote = "origin"
	p.EpicID = "COD-7108"
	p.exit.epicBranch, p.exit.epicPushed = epic, true
	g := &exitGit{branch: "main", commits: map[string][]string{"main.." + epic: {"drifted"}}}
	p.Git = g

	p.settleEpicBranch(context.Background())

	if len(g.deleted) != 1 {
		t.Errorf("deleted = %v, want the empty epic branch dropped — the local base is only behind origin/main", g.deleted)
	}
	if len(g.pushes) != 0 || gh.createCalls != 0 {
		t.Errorf("pushed %v and opened %d PR(s) for an epic branch carrying no work", g.pushes, gh.createCalls)
	}
}

// A child's PR squash-merges into the epic branch on the remote, and nothing pulls
// that back into the local ref until the next child syncs. Exit hygiene must settle
// what the remote carries: the branch is kept and tracked by a draft PR, not dropped
// as empty and not pushed non-fast-forward.
func TestSettleEpicBranchKeepsWorkTheRemoteCarries(t *testing.T) {
	const epic = "epic/COD-7109-thing"
	work, origin := t.TempDir(), t.TempDir()
	gitRun(t, origin, "init", "--bare")
	gitRun(t, work, "init")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "branch", "-M", "main")
	gitRun(t, work, "remote", "add", "origin", origin)
	gitRun(t, work, "push", "origin", "main")
	gitRun(t, work, "branch", epic)
	gitRun(t, work, "push", "origin", epic)
	gitRun(t, work, "checkout", "-b", "child")
	writeRepoFile(t, work, "b.txt", "child\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "child work")
	gitRun(t, work, "push", "origin", "child:"+epic)
	gitRun(t, work, "checkout", "main")
	gitRun(t, work, "branch", "-D", "child")

	gh := &epicGitHub{createURL: "https://github.test/pr/15"}
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
	p.Git = ExecGit{Repo: work}
	p.GitHub = gh
	p.Remote = "origin"
	p.EpicID = "COD-7109"
	p.exit.epicBranch, p.exit.epicPushed = epic, true

	p.settleEpicBranch(context.Background())

	if branches := gitOut(t, work, "branch", "--format=%(refname:short)"); !strings.Contains(branches, epic) {
		t.Errorf("branches = %q, want %s kept — the remote copy carries a child's landed work", branches, epic)
	}
	if gh.createCalls != 1 || !gh.createDraft {
		t.Errorf("PR create calls = %d (draft %v), want the unfinished epic tracked by one draft PR", gh.createCalls, gh.createDraft)
	}
}

// Cutting a slice branch off a freshly created epic branch stacks work on it, so
// exit hygiene must stop treating that branch as its own to drop — dropping it
// would orphan the slice.
func TestSliceBranchStacksOnEpicBranch(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
	p.GitHub = &epicGitHub{}
	p.Remote = "origin"
	p.EpicID = "COD-7105"
	g := &exitGit{branch: "main"}
	p.Git = g

	branch, err := p.resolveBuildBranch(context.Background(), "COD-7106")
	if err != nil {
		t.Fatalf("resolveBuildBranch err = %v", err)
	}
	if !strings.HasPrefix(branch, "feature/COD-7106") {
		t.Fatalf("branch = %q, want the slice's feature branch", branch)
	}
	if p.exit.epicBranch == "" {
		t.Fatal("the epic branch was not resolved")
	}

	p.settleEpicBranch(context.Background())

	if len(g.deleted) != 0 {
		t.Errorf("deleted %v — dropping an epic branch a slice is stacked on orphans that slice", g.deleted)
	}
}

// A partial epic branch that already has an open PR is tracked: exit hygiene adopts
// that PR rather than opening a second one.
func TestSettleEpicBranchAdoptsExistingPR(t *testing.T) {
	gh := &openPRGitHub{url: "https://github.test/pr/9"}
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
	p.GitHub = gh
	p.Remote = "origin"
	p.EpicID = "COD-7102"
	p.exit.epicBranch = "epic/COD-7102-thing"
	p.Git = &exitGit{branch: "main", commits: map[string][]string{"origin/main..epic/COD-7102-thing": {"abc123"}}}

	p.settleEpicBranch(context.Background())

	if gh.createCalls != 0 {
		t.Errorf("opened %d PR(s) for a branch that already has one", gh.createCalls)
	}
}

type openPRGitHub struct {
	epicGitHub
	url string
}

func (g *openPRGitHub) PRURL(context.Context, string) (string, error) { return g.url, nil }

// TestEpicMergeMarksDraftReady: the epic ships through whatever PR it has, so a
// draft an earlier run's exit hygiene opened is taken out of draft before the merge
// — GitHub refuses to merge a draft.
func TestEpicMergeMarksDraftReady(t *testing.T) {
	gh := &epicGitHub{checks: []Check{{Name: "ci/test", Bucket: "pass"}}}
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
	p.GitHub = gh
	p.EpicID = "COD-7103"
	p.AutoMerge = true
	p.MergeMethod = "squash"

	merged, err := p.epicCIAndMerge(context.Background(), "https://github.test/pr/7")
	if err != nil {
		t.Fatalf("epicCIAndMerge err = %v", err)
	}
	if !merged {
		t.Fatal("expected the epic to merge")
	}
	if gh.readyCalls != 1 {
		t.Errorf("gh pr ready calls = %d, want 1 — a draft PR cannot be merged", gh.readyCalls)
	}
}

// A second ExitCleanup is a no-op: the first consumed everything it restores, so the
// epic branch it pushed and opened a PR for is not pushed and PR'd all over again.
func TestExitCleanupIsIdempotent(t *testing.T) {
	const (
		id   = "COD-7107"
		epic = "epic/COD-7107-thing"
	)
	gh := &epicGitHub{createURL: "https://github.test/pr/11"}
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
	p.GitHub = gh
	p.Remote = "origin"
	p.EpicID = id
	g := &exitGit{branch: "main", epicFound: epic, commits: map[string][]string{"origin/main.." + epic: {"abc123"}}}
	p.Git = g

	ctx := context.Background()
	p.armExitCleanup(ctx)
	g.branch = epic
	p.exit.epicBranch, p.exit.epicPushed = epic, true

	p.ExitCleanup(ctx)
	p.ExitCleanup(ctx)

	if len(g.checkouts) != 1 {
		t.Errorf("checkouts = %v, want the start branch checked out once", g.checkouts)
	}
	if len(g.pushes) != 1 {
		t.Errorf("pushes = %v, want the epic branch pushed once", g.pushes)
	}
	if gh.createCalls != 1 {
		t.Errorf("PR create calls = %d, want 1", gh.createCalls)
	}
}

// TestExitCleanupAgainstRealGit is the acceptance case: a run interrupted after it
// stashed the user's WIP and cut an epic branch leaves the repo on the branch the
// user started on, with an empty stash and no dangling epic branch.
func TestExitCleanupAgainstRealGit(t *testing.T) {
	work := t.TempDir()
	gitRun(t, work, "init")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "branch", "-M", "main")
	gitRun(t, work, "checkout", "-b", "feature/mine")
	writeRepoFile(t, work, "a.txt", "my wip\n")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Git = ExecGit{Repo: work}
	p.Remote = "origin"
	p.AutoStash = true
	p.EpicID = "COD-7104"

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase err = %v", err)
	}
	gitRun(t, work, "checkout", "-b", "epic/COD-7104-thing")
	p.exit.epicBranch, p.exit.epicPushed = "epic/COD-7104-thing", true

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.ExitCleanup(ctx)

	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/mine" {
		t.Errorf("HEAD = %q, want the branch the run started on", got)
	}
	if got := gitOut(t, work, "stash", "list"); got != "" {
		t.Errorf("stash list = %q, want the run's autostash popped back", got)
	}
	if got := readRepoFile(t, work, "a.txt"); got != "my wip\n" {
		t.Errorf("a.txt = %q, want the user's WIP back", got)
	}
	if branches := gitOut(t, work, "branch", "--format=%(refname:short)"); strings.Contains(branches, "epic/") {
		t.Errorf("branches = %q, want the empty epic branch dropped", branches)
	}
}

// A run interrupted with work of its own still uncommitted must leave that work on the
// branch that owns it: git carries uncommitted changes across a checkout, so leftovers
// would otherwise ride onto the user's branch, collide with the stash pop and strand the
// run's work off its own branch.
func TestExitCleanupPreservesRunLeftovers(t *testing.T) {
	const branch = "feature/COD-9003-thing"
	work := t.TempDir()
	gitRun(t, work, "init")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "branch", "-M", "main")
	gitRun(t, work, "checkout", "-b", "feature/mine")
	writeRepoFile(t, work, "a.txt", "my wip\n")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Git = ExecGit{Repo: work}
	p.Remote = "origin"
	p.AutoStash = true

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase err = %v", err)
	}
	gitRun(t, work, "checkout", "-b", branch)
	writeRepoFile(t, work, "a.txt", "agent work\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.ExitCleanup(ctx)

	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/mine" {
		t.Errorf("HEAD = %q, want the branch the run started on", got)
	}
	if got := gitOut(t, work, "stash", "list"); got != "" {
		t.Errorf("stash list = %q, want the run's autostash popped back", got)
	}
	if got := readRepoFile(t, work, "a.txt"); got != "my wip\n" {
		t.Errorf("a.txt = %q, want the user's WIP back", got)
	}
	if got := gitOut(t, work, "show", branch+":a.txt"); got != "agent work" {
		t.Errorf("%s:a.txt = %q, want the run's leftovers committed on its own branch", branch, got)
	}
}

// Only work the run itself left on its own branch is preserved — the user's WIP never
// is. A cleanup with no branch to return to (a second deferred call, or a signal landing
// before the run recorded one) moves nothing, so it must hand the working copy back
// exactly as it found it.
func TestExitCleanupNeverCommitsUserWIP(t *testing.T) {
	t.Run("a second call after the WIP is restored", func(t *testing.T) {
		work := newWIPRepo(t)
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		p.Git = ExecGit{Repo: work}
		p.Remote = "origin"
		p.AutoStash = true

		if err := p.EnsureCleanBase(context.Background()); err != nil {
			t.Fatalf("EnsureCleanBase err = %v", err)
		}
		p.ExitCleanup(context.Background())
		head := gitOut(t, work, "rev-parse", "HEAD")

		p.ExitCleanup(context.Background())

		assertWIPUncommitted(t, work, head)
	})

	t.Run("interrupted before the run recorded a branch", func(t *testing.T) {
		work := newWIPRepo(t)
		head := gitOut(t, work, "rev-parse", "HEAD")
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		p.Git = ExecGit{Repo: work}
		p.Remote = "origin"
		p.AutoStash = true

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := p.EnsureCleanBase(ctx); err == nil {
			t.Fatal("EnsureCleanBase err = nil, want the cancelled context to abort it")
		}

		p.ExitCleanup(ctx)

		assertWIPUncommitted(t, work, head)
	})
}

// newWIPRepo is the state a user starts a run from: parked on a personal branch with
// uncommitted tracked changes.
func newWIPRepo(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	gitRun(t, work, "init")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "branch", "-M", "main")
	gitRun(t, work, "checkout", "-b", "feature/mine")
	writeRepoFile(t, work, "a.txt", "my wip\n")
	return work
}

func assertWIPUncommitted(t *testing.T, work, head string) {
	t.Helper()
	if got := gitOut(t, work, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD = %s, want %s — the user's WIP was committed to their branch", got, head)
	}
	if gitOut(t, work, "status", "--porcelain") == "" {
		t.Error("working tree is clean, want the user's WIP still uncommitted in it")
	}
}

// A resumed run starts on the ticket branch the user was parked on, and the epic flow
// moves HEAD onto the epic branch from there. Exit hygiene owes that checkout back —
// and until it does, the empty epic branch is the one HEAD sits on, which git refuses
// to delete.
func TestExitCleanupRestoresResumedTicketBranch(t *testing.T) {
	const (
		id   = "COD-9001"
		epic = "epic/COD-9002-thing"
	)
	work := t.TempDir()
	gitRun(t, work, "init")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "branch", "-M", "main")
	gitRun(t, work, "checkout", "-b", "feature/"+id+"-thing")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Git = ExecGit{Repo: work}
	p.Remote = "origin"
	p.EpicID = "COD-9002"
	if err := p.State.Set(id, "PHASE", state.Building); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.armExitCleanup(ctx)
	gitRun(t, work, "checkout", "-b", epic)
	p.exit.epicBranch = epic
	cancel()

	p.ExitCleanup(ctx)

	if got := gitOut(t, work, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/"+id+"-thing" {
		t.Errorf("HEAD = %q, want the ticket branch the resume started on", got)
	}
	if branches := gitOut(t, work, "branch", "--format=%(refname:short)"); strings.Contains(branches, "epic/") {
		t.Errorf("branches = %q, want the empty epic branch dropped", branches)
	}
}

// A signal landing while the conflict-resolution agent works leaves the epic branch
// mid-merge with conflict markers in the tree. Exit hygiene must abort that merge, not
// preserve it: committing it would land the markers on the branch the release resumes
// from. The checkpoint stays releasing so the next run re-enters the finalize.
func TestExitCleanupAbortsMergeInsteadOfCommittingConflictMarkers(t *testing.T) {
	const (
		id   = "COD-9100"
		epic = "epic/COD-9100-thing"
	)
	work := t.TempDir()
	gitRun(t, work, "init")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "branch", "-M", "main")

	gitRun(t, work, "checkout", "-b", epic)
	writeRepoFile(t, work, "a.txt", "epic work\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "epic work")
	gitRun(t, work, "checkout", "main")
	writeRepoFile(t, work, "a.txt", "base drift\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "base drift")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Git = ExecGit{Repo: work}
	p.Remote = "origin"
	p.EpicID = id
	if err := p.State.Set(id, "PHASE", state.Releasing); err != nil {
		t.Fatal(err)
	}
	if err := p.State.Set(id, "RELEASE", state.ReleaseActive); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.armExitCleanup(ctx)
	gitRun(t, work, "checkout", epic)
	p.exit.epicBranch = epic
	if err := exec.Command("git", "-C", work, "merge", "--no-edit", "main").Run(); err == nil {
		t.Fatal("merge main into the epic branch succeeded, want the conflict the agent is resolving")
	}
	if !strings.Contains(readRepoFile(t, work, "a.txt"), "<<<<<<<") {
		t.Fatal("a.txt carries no conflict markers, want the tree the killed agent leaves behind")
	}
	epicTip := gitOut(t, work, "rev-parse", epic)
	cancel()

	p.ExitCleanup(ctx)

	if err := exec.Command("git", "-C", work, "rev-parse", "-q", "--verify", "MERGE_HEAD").Run(); err == nil {
		t.Error("MERGE_HEAD is still present, want the half-merge aborted")
	}
	if got := gitOut(t, work, "rev-parse", epic); got != epicTip {
		t.Errorf("%s = %s, want %s — the half-merge must not become a commit", epic, got, epicTip)
	}
	if got := gitOut(t, work, "log", "-p", "--all"); strings.Contains(got, "<<<<<<<") {
		t.Error("a commit carries conflict markers, want the merge aborted instead of preserved")
	}
	if got := p.State.Get(id, "PHASE"); got != state.Releasing {
		t.Errorf("epic PHASE = %q, want %q so the next run re-enters the finalize", got, state.Releasing)
	}
}

func readRepoFile(t *testing.T, dir, name string) string {
	t.Helper()
	out, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(out)
}
