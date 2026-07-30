package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// localGit records the git the delivery paths reach for, and reports whether the
// repo has a remote at all — the one fact the preflight turns into a delivery mode.
type localGit struct {
	fakeGit
	hasRemote     bool
	remoteErr     error
	remoteLookups int
	branch        string
	commits       int
	pushes        int
	pushedRefs    []string
	checkoutErr   error
	squashBranch  string
	squashMessage string
	squashCalls   int
	squashErr     error
	checkedOut    []string
}

func (g *localGit) RemoteExists(context.Context, string) (bool, error) {
	g.remoteLookups++
	return g.hasRemote, g.remoteErr
}

func (g *localGit) CurrentBranch(context.Context) (string, error) {
	if g.branch == "" {
		return "main", nil
	}
	return g.branch, nil
}

func (g *localGit) Commit(context.Context, string, bool) error {
	g.commits++
	return nil
}

func (g *localGit) Push(_ context.Context, _ string, ref string, _ bool) error {
	g.pushes++
	g.pushedRefs = append(g.pushedRefs, ref)
	return nil
}

func (g *localGit) SquashMerge(_ context.Context, branch, message string) error {
	g.squashCalls++
	g.squashBranch, g.squashMessage = branch, message
	return g.squashErr
}

func (g *localGit) Checkout(_ context.Context, ref string, _ bool) error {
	if g.checkoutErr != nil {
		return g.checkoutErr
	}
	g.checkedOut = append(g.checkedOut, ref)
	return nil
}

// countingGitHub counts every gh call, so a remote-less run can assert it made none.
type countingGitHub struct {
	epicGitHub
	touched int
}

func (c *countingGitHub) PRURL(context.Context, string) (string, error) {
	c.touched++
	return "", nil
}

func (c *countingGitHub) MergedPRURL(ctx context.Context, branch string) (string, error) {
	c.touched++
	return c.epicGitHub.MergedPRURL(ctx, branch)
}

func (c *countingGitHub) CreatePR(ctx context.Context, base, head, title, body string) (string, error) {
	c.touched++
	return c.epicGitHub.CreatePR(ctx, base, head, title, body)
}

func (c *countingGitHub) PRState(context.Context, string) (string, error) {
	c.touched++
	return c.prState, nil
}

func (c *countingGitHub) Checks(context.Context, string) ([]Check, error) {
	c.touched++
	return c.checks, nil
}

func (c *countingGitHub) Merge(ctx context.Context, pr, method string, deleteBranch bool) error {
	c.touched++
	return c.epicGitHub.Merge(ctx, pr, method, deleteBranch)
}

func localTestPipeline(t *testing.T, git Git, gh GitHub, tr tracker.Tracker) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	return &Pipeline{
		Git:                 git,
		GitHub:              gh,
		Tracker:             tr,
		State:               state.NewStore(dir),
		RunsDir:             dir,
		Base:                "main",
		Remote:              "origin",
		Prefix:              "COD",
		AutoMerge:           true,
		RequireCI:           config.CIGateOn,
		MergeMethod:         "squash",
		DeterministicCommit: true,
	}
}

// A repo with no origin is a local-only project, not a broken one: the slice is
// committed and left on its branch, with no push attempt and no PR to open.
func TestCommitAndPRSkipsPushAndPRWithoutRemote(t *testing.T) {
	id := "COD-96401"
	git := &localGit{}
	gh := &countingGitHub{}
	p := localTestPipeline(t, git, gh, &epicTracker{})

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if git.pushes != 0 {
		t.Errorf("pushed %d times to a nonexistent remote, want 0", git.pushes)
	}
	if gh.touched != 0 {
		t.Errorf("made %d gh calls without a remote, want 0", gh.touched)
	}
	if got := p.State.Get(id, "PHASE"); got != state.PROpen {
		t.Errorf("PHASE = %q, want %q", got, state.PROpen)
	}
	if got := p.State.Get(id, "DELIVERY"); got != deliveryLocal {
		t.Errorf("DELIVERY = %q, want %q", got, deliveryLocal)
	}
}

// The remote path is untouched: a repo that has origin still pushes and opens a PR.
func TestCommitAndPRPushesWhenRemoteExists(t *testing.T) {
	id := "COD-96402"
	git := &localGit{hasRemote: true}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: "https://github.test/pr/7"}}
	p := localTestPipeline(t, git, gh, &epicTracker{})
	if err := p.State.Set(id, "BRANCH", "feature/COD-96402-thing"); err != nil {
		t.Fatal(err)
	}

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if git.pushes != 1 {
		t.Errorf("pushed %d times, want 1", git.pushes)
	}
	if gh.createCalls != 1 {
		t.Errorf("created %d PRs, want 1", gh.createCalls)
	}
	if got := p.State.Get(id, "PR_URL"); got != "https://github.test/pr/7" {
		t.Errorf("PR_URL = %q, want the created PR", got)
	}
	if got := p.State.Get(id, "DELIVERY"); got != "" {
		t.Errorf("DELIVERY = %q, want it unset on a remote repo", got)
	}
}

// Without a remote there is no PR to gate and no CI to wait for: the branch is
// squash-merged into its base right here and the ticket settles Done.
func TestCIAndMergeLandsLocallyWithoutRemote(t *testing.T) {
	cases := []struct {
		name, epicID, wantBase string
	}{
		{name: "slice lands on the base branch", wantBase: "main"},
		{name: "epic slice lands on the epic branch", epicID: "COD-1", wantBase: "epic/COD-1-rebuild"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := "COD-96403"
			git := &localGit{}
			gh := &countingGitHub{}
			tr := &epicTracker{}
			p := localTestPipeline(t, git, gh, tr)
			p.EpicID = c.epicID
			p.epicBranch = "epic/COD-1-rebuild"
			if err := p.State.Set(id, "BRANCH", "feature/COD-96403-thing"); err != nil {
				t.Fatal(err)
			}

			if err := p.CIAndMerge(context.Background(), id); err != nil {
				t.Fatalf("CIAndMerge = %v, want nil", err)
			}
			if gh.touched != 0 {
				t.Errorf("made %d gh calls without a remote, want 0", gh.touched)
			}
			if git.squashCalls != 1 {
				t.Fatalf("squash-merged %d times, want 1", git.squashCalls)
			}
			if git.squashBranch != "feature/COD-96403-thing" {
				t.Errorf("merged %q, want the feature branch", git.squashBranch)
			}
			if last := git.checkedOut[len(git.checkedOut)-1]; last != c.wantBase {
				t.Errorf("merged onto %q, want %q", last, c.wantBase)
			}
			if !strings.Contains(git.squashMessage, "Refs: "+id) {
				t.Errorf("squash message %q does not reference the ticket", git.squashMessage)
			}
			if got := p.State.Get(id, "PHASE"); got != state.Merged {
				t.Errorf("PHASE = %q, want %q", got, state.Merged)
			}
			if set := tr.setFor(id); set == nil || set.stage != tracker.StageDone {
				t.Errorf("tracker status = %+v, want Done", set)
			}
		})
	}
}

// A local merge git refuses is a verified dead end, not a fault: quarantine and
// leave the feature branch with its work exactly where it is.
func TestCIAndMergeLocalQuarantinesOnRefusedMerge(t *testing.T) {
	id := "COD-96404"
	git := &localGit{squashErr: errors.New("merge --squash: conflicts in web/app.tsx")}
	tr := &epicTracker{}
	p := localTestPipeline(t, git, &countingGitHub{}, tr)
	if err := p.State.Set(id, "BRANCH", "feature/COD-96404-thing"); err != nil {
		t.Fatal(err)
	}

	err := p.CIAndMerge(context.Background(), id)

	if !isGiveUp(err) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("quarantined %d times, want 1", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want %q", got, state.Quarantined)
	}
}

// Quarantining a slice parked on its feature branch still commits the attempt so
// nothing is lost, but a repo with no remote has nowhere to push it.
func TestQuarantineSavesWIPWithoutPushingWithoutRemote(t *testing.T) {
	id := "COD-96406"
	git := &localGit{branch: "feature/COD-96406-thing"}
	p := localTestPipeline(t, git, &countingGitHub{}, &epicTracker{})

	if err := p.giveUp(context.Background(), id, "verify failed"); !isGiveUp(err) {
		t.Fatalf("giveUp = %v, want a *GiveUpError", err)
	}
	if git.commits != 1 {
		t.Errorf("committed the attempt %d times, want 1 — the WIP must be preserved", git.commits)
	}
	if git.pushes != 0 {
		t.Errorf("pushed %d times to a nonexistent remote, want 0", git.pushes)
	}
}

// AUTO_MERGE=0 still means the operator lands the work themselves — local delivery
// does not turn the setting off.
func TestLandLocallyLeavesTheMergeToTheOperatorWhenAutoMergeIsOff(t *testing.T) {
	id := "COD-96405"
	git := &localGit{}
	p := localTestPipeline(t, git, &countingGitHub{}, &epicTracker{})
	p.AutoMerge = false
	if err := p.State.Set(id, "BRANCH", "feature/COD-96405-thing"); err != nil {
		t.Fatal(err)
	}

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if git.squashCalls != 0 {
		t.Errorf("squash-merged %d times with AUTO_MERGE=0, want 0", git.squashCalls)
	}
	if got := p.State.Get(id, "PR_STATUS"); got != prStatusAwaitingMerge {
		t.Errorf("PR_STATUS = %q, want %q", got, prStatusAwaitingMerge)
	}
}

// A complete epic on a remote-less repo ships the same way a slice does: epic
// branch squash-merged into the base locally, no PR and no CI gate.
func TestFinalizeEpicMergesLocallyWithoutRemote(t *testing.T) {
	tr := &epicTracker{
		title: "Checkout rebuild",
		subs:  []tracker.SubIssue{{ID: "COD-2", Title: "first"}},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
		},
	}
	git := &localGit{}
	gh := &countingGitHub{}
	p := localTestPipeline(t, git, gh, tr)
	p.EpicID = "COD-1"
	p.epicBranch = "epic/COD-1-checkout-rebuild"

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic = %v, want nil", err)
	}
	if gh.touched != 0 {
		t.Errorf("made %d gh calls without a remote, want 0", gh.touched)
	}
	if git.squashBranch != "epic/COD-1-checkout-rebuild" {
		t.Errorf("merged %q, want the epic branch", git.squashBranch)
	}
	if last := git.checkedOut[len(git.checkedOut)-1]; last != "main" {
		t.Errorf("merged onto %q, want main", last)
	}
	if got := p.State.Get("COD-1", "PHASE"); got != state.Merged {
		t.Errorf("epic PHASE = %q, want %q", got, state.Merged)
	}
	if got := p.State.Get("COD-1", "PR_URL"); got != "" {
		t.Errorf("epic PR_URL = %q, want it unset — there is no PR", got)
	}
	if tr.setStatus != tracker.StageDone || !strings.Contains(tr.setExtra, localDeliveryNote) {
		t.Errorf("epic closed as %q %q, want Done naming local delivery", tr.setStatus, tr.setExtra)
	}
}

// The epic honours AUTO_MERGE=0 exactly as a slice does: the epic branch is left
// on the operator's desk, unmerged and still open.
func TestFinalizeEpicLeavesTheMergeToTheOperatorWhenAutoMergeIsOff(t *testing.T) {
	tr := &epicTracker{
		title:  "Checkout rebuild",
		subs:   []tracker.SubIssue{{ID: "COD-2", Title: "first"}},
		status: map[string]tracker.IssueStatus{"COD-2": tracker.StatusDone},
	}
	git := &localGit{}
	p := localTestPipeline(t, git, &countingGitHub{}, tr)
	p.AutoMerge = false
	p.EpicID = "COD-1"
	p.epicBranch = "epic/COD-1-checkout-rebuild"

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic = %v, want nil", err)
	}
	if git.squashCalls != 0 {
		t.Errorf("squash-merged %d times with AUTO_MERGE=0, want 0", git.squashCalls)
	}
	if tr.setStatus == tracker.StageDone {
		t.Error("epic closed Done, want it left open until the operator merges")
	}
}

// The preflight is one lookup per run: every later phase reads the cached verdict.
func TestLocalDeliveryPreflightResolvesOnce(t *testing.T) {
	git := &localGit{}
	p := localTestPipeline(t, git, &countingGitHub{}, &epicTracker{})
	ctx := context.Background()

	for range 3 {
		if !p.localDelivery(ctx) {
			t.Fatal("localDelivery = false, want true for a repo with no remote")
		}
	}
	if git.remoteLookups != 1 {
		t.Errorf("probed the remote %d times, want 1", git.remoteLookups)
	}
}

// A lookup git itself could not answer is not proof of a missing remote, so the
// run keeps the remote path rather than quietly stopping publishing its work.
func TestRemoteLookupFailureKeepsTheRemotePath(t *testing.T) {
	git := &localGit{remoteErr: errors.New("fatal: not a git repository")}
	p := localTestPipeline(t, git, &countingGitHub{}, &epicTracker{})

	if p.localDelivery(context.Background()) {
		t.Error("localDelivery = true on a failed lookup, want the remote path")
	}
}

// The two new git operations against real plumbing: a repo answers honestly about
// its remote, and the whole feature branch lands on the base as one commit.
func TestExecGitRemoteExistsAndSquashMergeAgainstRealGit(t *testing.T) {
	work := t.TempDir()
	gitRun(t, work, "init", "-b", "main")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "checkout", "-b", "feature/COD-964-local")
	writeRepoFile(t, work, "a.txt", "slice edit\n")
	gitRun(t, work, "commit", "-am", "wip one")
	writeRepoFile(t, work, "b.txt", "slice addition\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "wip two")
	gitRun(t, work, "checkout", "main")

	git := ExecGit{Repo: work}
	ctx := context.Background()

	switch exists, err := git.RemoteExists(ctx, "origin"); {
	case err != nil:
		t.Fatalf("RemoteExists err = %v, want an absent remote reported as (false, nil)", err)
	case exists:
		t.Fatal("RemoteExists = true, want false for a repo with no remote")
	}

	gitRun(t, work, "remote", "add", "origin", filepath.Join(t.TempDir(), "remote.git"))
	if exists, err := git.RemoteExists(ctx, "origin"); err != nil || !exists {
		t.Fatalf("RemoteExists = %v, %v; want true, nil once origin is configured", exists, err)
	}

	if err := git.SquashMerge(ctx, "feature/COD-964-local", "feat: thing\n\nRefs: COD-964"); err != nil {
		t.Fatalf("SquashMerge err = %v", err)
	}
	if got := gitOut(t, work, "status", "--porcelain"); got != "" {
		t.Errorf("working tree not clean after the local merge: %q", got)
	}
	if got := gitOut(t, work, "log", "-1", "--format=%s", "main"); got != "feat: thing" {
		t.Errorf("main tip subject = %q, want the squash message", got)
	}
	if got := gitOut(t, work, "rev-list", "--count", "main"); got != "2" {
		t.Errorf("main has %s commits, want 2 — the slice must land squashed", got)
	}
	if got := gitOut(t, work, "show", "--name-only", "--format=", "main"); !strings.Contains(got, "a.txt") || !strings.Contains(got, "b.txt") {
		t.Errorf("squash commit touches %q, want every file the branch changed", got)
	}
}

// A conflicting local merge must leave nothing behind: the base keeps its own tip
// and the tree is not parked mid-merge for the next run to trip over.
func TestExecGitSquashMergeResetsOnConflict(t *testing.T) {
	work := t.TempDir()
	gitRun(t, work, "init", "-b", "main")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "init")
	gitRun(t, work, "checkout", "-b", "feature/COD-964-clash")
	writeRepoFile(t, work, "a.txt", "theirs\n")
	gitRun(t, work, "commit", "-am", "branch edit")
	gitRun(t, work, "checkout", "main")
	writeRepoFile(t, work, "a.txt", "ours\n")
	gitRun(t, work, "commit", "-am", "base edit")

	if err := (ExecGit{Repo: work}).SquashMerge(context.Background(), "feature/COD-964-clash", "feat: thing"); err == nil {
		t.Fatal("SquashMerge = nil on conflicting branches, want an error")
	}
	if got := gitOut(t, work, "status", "--porcelain"); got != "" {
		t.Errorf("working tree = %q after a refused merge, want it clean", got)
	}
	if got := gitOut(t, work, "log", "-1", "--format=%s", "main"); got != "base edit" {
		t.Errorf("main tip = %q, want the base's own commit — nothing may land", got)
	}
}
