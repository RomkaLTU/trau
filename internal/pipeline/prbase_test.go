package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// makeChildRepos lays out a Folder repo: a plain directory whose git repos are the
// run's ship targets.
func makeChildRepos(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// prBaseGit is a localGit whose remote copy of the BASE branch may lag behind the
// commit a PR would be diffed from, and which can be scripted to stay behind after
// the repair push, to reject that push, or to fail the ls-remote read outright.
type prBaseGit struct {
	localGit
	carries   bool // whether the remote base contains the pin
	pushFixes bool // whether pushing the base makes it contain the pin
	pushErr   error
	shaErr    error
	shaReads  int
}

func (g *prBaseGit) RemoteSHA(context.Context, string, string) (string, error) {
	g.shaReads++
	if g.shaErr != nil {
		return "", g.shaErr
	}
	return prBaseRemoteTip, nil
}

func (g *prBaseGit) IsAncestor(context.Context, string, string) (bool, error) {
	return g.carries, nil
}

func (g *prBaseGit) Push(ctx context.Context, remote, ref string, noVerify bool) error {
	if ref == prBaseBranch && g.pushErr != nil {
		return g.pushErr
	}
	if err := g.localGit.Push(ctx, remote, ref, noVerify); err != nil {
		return err
	}
	if ref == prBaseBranch && g.pushFixes {
		g.carries = true
	}
	return nil
}

// currentBaseSHA is what ls-remote answers for a remote that has the base branch and
// carries everything local — the ordinary state every fake not exercising base drift
// reports, and nothing else: a branch the remote lacks is an empty answer.
func currentBaseSHA(branch string) string {
	if branch == prBaseBranch {
		return prBaseRemoteTip
	}
	return ""
}

// baseCurrentGit is a fakeGit on such a remote, for the tests that only need the
// PR-base gate to wave them through.
type baseCurrentGit struct{ fakeGit }

func (baseCurrentGit) RemoteSHA(_ context.Context, _, branch string) (string, error) {
	return currentBaseSHA(branch), nil
}

const (
	prBaseBranch    = "main"
	prBaseRemoteTip = "b41d90ffb41d90ffb41d90ffb41d90ffb41d90ff"
	prBaseForkPoint = "1c77e5a41c77e5a41c77e5a41c77e5a41c77e5a4"
	prBaseRunBranch = "feature/COD-4-base-gate"
	prBaseTicketID  = "COD-4"
	prBasePRCreated = "https://github.test/pr/512"
)

func newSlicePRPipeline(t *testing.T, git Git, gh GitHub) *Pipeline {
	t.Helper()
	p := localTestPipeline(t, git, gh, &epicTracker{title: "Base gate slice"})
	if err := p.State.Set(prBaseTicketID, "BRANCH", prBaseRunBranch); err != nil {
		t.Fatal(err)
	}
	if err := p.State.Set(prBaseTicketID, "BASE_SHA", prBaseForkPoint); err != nil {
		t.Fatal(err)
	}
	return p
}

// The remote base already carries the commit the run was cut from, so the PR opens
// untouched — the gate is a repair for drift, not a push on every slice.
func TestCommitAndPROpensSlicePRWhenRemoteBaseCarriesForkPoint(t *testing.T) {
	git := &prBaseGit{localGit: localGit{hasRemote: true, branch: prBaseRunBranch}, carries: true}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: prBasePRCreated}}
	p := newSlicePRPipeline(t, git, gh)

	if err := p.CommitAndPR(context.Background(), prBaseTicketID); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if gh.createCalls != 1 || gh.base != prBaseBranch {
		t.Errorf("created %d PR(s) against %q, want 1 against the base branch", gh.createCalls, gh.base)
	}
	if got := git.pushedRefs; len(got) != 1 || got[0] != prBaseRunBranch {
		t.Errorf("pushedRefs = %v, want only the run's own branch", got)
	}
	if git.shaReads == 0 {
		t.Error("the remote base tip was never read — the PR base went unverified")
	}
}

// The incident: the local base carries commits that were never pushed, so GitHub
// would diff a one-commit slice against a base older than its fork point. The base
// is fast-forwarded first, then the PR opens as usual.
func TestCommitAndPRPushesStaleRemoteBaseBeforeOpeningSlicePR(t *testing.T) {
	git := &prBaseGit{localGit: localGit{hasRemote: true, branch: prBaseRunBranch}, pushFixes: true}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: prBasePRCreated}}
	p := newSlicePRPipeline(t, git, gh)

	if err := p.CommitAndPR(context.Background(), prBaseTicketID); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if got := git.pushedRefs; len(got) != 2 || got[1] != prBaseBranch {
		t.Errorf("pushedRefs = %v, want the base repaired after the run's own push", got)
	}
	if gh.createCalls != 1 || gh.base != prBaseBranch {
		t.Errorf("created %d PR(s) against %q, want 1 against the repaired base", gh.createCalls, gh.base)
	}
}

// A protected base rejects the repair push. The phase fails before any PR exists,
// naming the remote branch and the missing commit, and the slice stays resumable
// with its work pushed.
func TestCommitAndPRRefusesSlicePRWhenBaseRepairPushIsRejected(t *testing.T) {
	git := &prBaseGit{
		localGit: localGit{hasRemote: true, branch: prBaseRunBranch},
		pushErr:  errors.New("! [remote rejected] main -> main (protected branch hook declined)"),
	}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: prBasePRCreated}}
	p := newSlicePRPipeline(t, git, gh)

	err := p.CommitAndPR(context.Background(), prBaseTicketID)
	if err == nil {
		t.Fatal("CommitAndPR = nil, want a stale-base failure")
	}
	for _, want := range []string{"origin/main", prBaseForkPoint, "remote rejected"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("CommitAndPR = %v, want a message carrying %q", err, want)
		}
	}
	if gh.createCalls != 0 {
		t.Errorf("created %d PR(s) against a stale base, want 0", gh.createCalls)
	}
	if got := p.State.Get(prBaseTicketID, "PHASE"); got == state.PROpen {
		t.Error("PHASE = pr_open, want the phase left short of a PR that was never opened")
	}
}

// A remote that cannot be read leaves the base unproven, which is not a licence to
// open the PR anyway: the phase fails with the instrumentation error.
func TestCommitAndPRFailsSlicePRWhenRemoteBaseCannotBeRead(t *testing.T) {
	git := &prBaseGit{
		localGit: localGit{hasRemote: true, branch: prBaseRunBranch},
		shaErr:   errors.New("ls-remote: could not read from remote repository"),
	}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: prBasePRCreated}}
	p := newSlicePRPipeline(t, git, gh)

	err := p.CommitAndPR(context.Background(), prBaseTicketID)
	if err == nil || !strings.Contains(err.Error(), "read origin/main") {
		t.Fatalf("CommitAndPR = %v, want the unreadable-remote failure", err)
	}
	if gh.createCalls != 0 {
		t.Errorf("created %d PR(s) against an unverified base, want 0", gh.createCalls)
	}
}

// The epic PR is opened against the base branch too — finalize and the exit-hygiene
// draft both go through this gate, with the local base tip as the pin.
func TestEnsureEpicPRPushesStaleBaseBeforeCreating(t *testing.T) {
	git := &prBaseGit{localGit: localGit{hasRemote: true, branch: epicBaseBranch}, pushFixes: true}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: "https://github.test/pr/77"}}
	p := localTestPipeline(t, git, gh, &epicTracker{title: "Epic"})
	p.EpicID = "COD-1"

	url, _, err := p.ensureEpicPR(context.Background(), epicBaseBranch, true)
	if err != nil {
		t.Fatalf("ensureEpicPR = %v, want nil", err)
	}
	if url != "https://github.test/pr/77" {
		t.Errorf("ensureEpicPR url = %q, want the created PR", url)
	}
	if got := git.pushedRefs; len(got) != 1 || got[0] != prBaseBranch {
		t.Errorf("pushedRefs = %v, want the base repaired before the epic PR", got)
	}
}

// A repo with no remote has no base to gate and no PR to protect, so the gate never
// reaches for ls-remote.
func TestLocalDeliveryNeverGatesThePRBase(t *testing.T) {
	git := &prBaseGit{localGit: localGit{branch: prBaseRunBranch}}
	gh := &countingGitHub{}
	p := newSlicePRPipeline(t, git, gh)

	if err := p.CommitAndPR(context.Background(), prBaseTicketID); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if git.shaReads != 0 {
		t.Errorf("read the remote base tip %d time(s) on a repo with no remote", git.shaReads)
	}
}

// Each Folder child is its own repo with its own remote: the gate runs per child,
// through that child's git, and leaves the children whose base is current alone.
func TestFolderChildPRGatesEachChildOnItsOwnBase(t *testing.T) {
	id := "COD-93020"
	branch := "feature/COD-93020-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-stale":   {baseBehind: true, pushFixes: true},
		"api-current": {},
	}
	ghs := map[string]*epicGitHub{
		"api-stale":   {createURL: "https://github.com/acme/api-stale/pull/9"},
		"api-current": {createURL: "https://github.com/acme/api-current/pull/4"},
	}
	makeChildRepos(t, root, "api-stale", "api-current")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-stale"].status = " M stale.go"
	gits["api-current"].status = " M current.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if got := gits["api-stale"].pushed; len(got) != 2 || got[1] != prBaseBranch {
		t.Errorf("api-stale pushed = %v, want its own base repaired after the slice branch", got)
	}
	if got := gits["api-current"].pushed; len(got) != 1 || got[0] != branch {
		t.Errorf("api-current pushed = %v, want only the slice branch", got)
	}
	for name, gh := range ghs {
		if gh.createCalls != 1 {
			t.Errorf("%s opened %d PR(s), want 1", name, gh.createCalls)
		}
	}
}

// A child whose base cannot be brought current fails the ship before its PR exists.
func TestFolderChildPRRefusedWhenChildBaseStaysStale(t *testing.T) {
	id := "COD-93021"
	branch := "feature/COD-93021-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{"api-stale": {baseBehind: true}}
	ghs := map[string]*epicGitHub{"api-stale": {createURL: "https://github.com/acme/api-stale/pull/9"}}
	makeChildRepos(t, root, "api-stale")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-stale"].status = " M stale.go"

	err := p.CommitAndPR(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "origin/main") {
		t.Fatalf("CommitAndPR = %v, want a stale-base failure naming the child's remote base", err)
	}
	if ghs["api-stale"].createCalls != 0 {
		t.Errorf("created %d PR(s) against a stale base, want 0", ghs["api-stale"].createCalls)
	}
}

// Run start is the earliest point the origin==local invariant can be restored: a
// local base ahead of the remote is fast-forwarded before any branch is cut.
func TestEnsureCleanBasePushesLocalBaseAheadOfRemote(t *testing.T) {
	git := &prBaseGit{localGit: localGit{hasRemote: true}}
	p := localTestPipeline(t, git, &countingGitHub{}, &epicTracker{})

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase = %v, want nil", err)
	}
	if got := git.pushedRefs; len(got) != 1 || got[0] != prBaseBranch {
		t.Errorf("pushedRefs = %v, want the base fast-forwarded onto the remote", got)
	}
}

// That heal is best-effort — the PR-open gate is the hard guarantee — so a base the
// remote refuses is logged and the run carries on.
func TestEnsureCleanBaseContinuesWhenBaseFastForwardIsRejected(t *testing.T) {
	git := &prBaseGit{
		localGit: localGit{hasRemote: true},
		pushErr:  errors.New("! [remote rejected] main -> main (protected branch hook declined)"),
	}
	p := localTestPipeline(t, git, &countingGitHub{}, &epicTracker{})
	log := &logRenderer{}
	p.Renderer = log

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase = %v, want the run to continue", err)
	}
	if len(git.pushedRefs) != 0 {
		t.Errorf("pushedRefs = %v, want the rejected push to have landed nothing", git.pushedRefs)
	}
	if !log.contains("couldn't push main to origin") {
		t.Error("the rejected fast-forward went unlogged")
	}
}
