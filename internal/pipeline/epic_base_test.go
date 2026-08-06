package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// epicBaseGit is a localGit whose remote epic branch may lag behind the commit the
// run was cut from, and which can be scripted to still lag after the repair push.
type epicBaseGit struct {
	localGit
	tip        string // the epic tip the remote reports
	carriesPin bool   // whether that tip contains the run's fork point
	pushFixes  bool   // whether pushing the epic branch makes it contain it
	shaReads   int
}

func (g *epicBaseGit) RemoteSHA(context.Context, string, string) (string, error) {
	g.shaReads++
	return g.tip, nil
}

func (g *epicBaseGit) IsAncestor(context.Context, string, string) (bool, error) {
	return g.carriesPin, nil
}

func (g *epicBaseGit) Push(ctx context.Context, remote, ref string, noVerify bool) error {
	if err := g.localGit.Push(ctx, remote, ref, noVerify); err != nil {
		return err
	}
	if strings.HasPrefix(ref, "epic/") && g.pushFixes {
		g.carriesPin = true
	}
	return nil
}

const (
	epicBaseBranch  = "epic/COD-1-checkout-rebuild"
	childRunBranch  = "feature/COD-2-child-slice"
	childForkPoint  = "4919ffd1c0de4919ffd1c0de4919ffd1c0de4919"
	staleRemoteTip  = "57d1122c0de57d1122c0de57d1122c0de57d1122"
	childPRCreated  = "https://github.test/pr/412"
	childPRTicketID = "COD-2"
)

func newChildPRPipeline(t *testing.T, git Git, gh Delivery) *Pipeline {
	t.Helper()
	p := localTestPipeline(t, git, gh, &epicTracker{title: "Child slice"})
	p.EpicID = "COD-1"
	p.exit.epicBranch = epicBaseBranch
	if err := p.State.Set(childPRTicketID, "BRANCH", childRunBranch); err != nil {
		t.Fatal(err)
	}
	return p
}

// The remote epic already carries the commit the child was cut from, so the PR is
// opened against it untouched — the check is a repair for a lost push, not a new
// push on every child.
func TestCommitAndPROpensChildPRWhenEpicBaseCarriesForkPoint(t *testing.T) {
	git := &epicBaseGit{localGit: localGit{hasRemote: true, branch: childRunBranch}, tip: childForkPoint, carriesPin: true}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: childPRCreated}}
	p := newChildPRPipeline(t, git, gh)
	if err := p.State.Set(childPRTicketID, "BASE_SHA", childForkPoint); err != nil {
		t.Fatal(err)
	}

	if err := p.CommitAndPR(context.Background(), childPRTicketID); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if gh.createCalls != 1 || gh.base != epicBaseBranch {
		t.Errorf("created %d PR(s) against %q, want 1 against the epic branch", gh.createCalls, gh.base)
	}
	if got := git.pushedRefs; len(got) != 1 || got[0] != childRunBranch {
		t.Errorf("pushedRefs = %v, want only the run's own branch", got)
	}
	if git.shaReads == 0 {
		t.Error("the remote epic tip was never read — the PR base went unverified")
	}
}

// syncEpicBest's push is best-effort, so a push that never landed leaves the remote
// epic behind the recorded fork point. GitHub would diff the child against that older
// base, so the epic branch is pushed before the PR is opened.
func TestCommitAndPRPushesStaleEpicBaseBeforeOpeningChildPR(t *testing.T) {
	git := &epicBaseGit{
		localGit:  localGit{hasRemote: true, branch: childRunBranch},
		tip:       staleRemoteTip,
		pushFixes: true,
	}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: childPRCreated}}
	p := newChildPRPipeline(t, git, gh)
	if err := p.State.Set(childPRTicketID, "BASE_SHA", childForkPoint); err != nil {
		t.Fatal(err)
	}

	if err := p.CommitAndPR(context.Background(), childPRTicketID); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if got := git.pushedRefs; len(got) != 2 || got[1] != epicBaseBranch {
		t.Errorf("pushedRefs = %v, want the epic branch repaired after the run's own push", got)
	}
	if gh.createCalls != 1 || gh.base != epicBaseBranch {
		t.Errorf("created %d PR(s) against %q, want 1 against the repaired epic branch", gh.createCalls, gh.base)
	}
}

// A remote epic that still misses the fork point after the push would produce a PR
// diffed against a stale base: fail loudly instead, leaving the ticket resumable
// with its work pushed and no PR opened.
func TestCommitAndPRRefusesChildPRWhenEpicBaseStaysStale(t *testing.T) {
	git := &epicBaseGit{localGit: localGit{hasRemote: true, branch: childRunBranch}, tip: staleRemoteTip}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: childPRCreated}}
	p := newChildPRPipeline(t, git, gh)
	if err := p.State.Set(childPRTicketID, "BASE_SHA", childForkPoint); err != nil {
		t.Fatal(err)
	}

	err := p.CommitAndPR(context.Background(), childPRTicketID)
	if err == nil || !strings.Contains(err.Error(), "does not carry "+childForkPoint) {
		t.Fatalf("CommitAndPR = %v, want a stale-epic-base failure naming the fork point", err)
	}
	if gh.createCalls != 0 {
		t.Errorf("created %d PR(s) against a stale base, want 0", gh.createCalls)
	}
	if got := p.State.Get(childPRTicketID, "PHASE"); got == state.PROpen {
		t.Error("PHASE = pr_open, want the phase left short of a PR that was never opened")
	}
}

// A checkpoint predating fork-point pinning has nothing to compare, so the PR is
// opened as before rather than blocked on a missing record.
func TestCommitAndPRSkipsEpicBaseCheckWithoutForkPoint(t *testing.T) {
	git := &epicBaseGit{localGit: localGit{hasRemote: true, branch: childRunBranch}, tip: staleRemoteTip}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: childPRCreated}}
	p := newChildPRPipeline(t, git, gh)

	if err := p.CommitAndPR(context.Background(), childPRTicketID); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if gh.createCalls != 1 {
		t.Errorf("created %d PR(s), want 1", gh.createCalls)
	}
	if git.shaReads != 0 {
		t.Errorf("read the remote epic tip %d time(s) without a fork point to check it against", git.shaReads)
	}
}
