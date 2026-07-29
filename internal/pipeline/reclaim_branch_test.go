package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A worktree an outside actor switched off the run's branch is reclaimed before
// the commit: the recorded branch is checked out again and the push names it
// explicitly, so nothing lands on whichever branch was left checked out.
func TestCommitAndPRReclaimsRecordedBranch(t *testing.T) {
	id := "COD-96403"
	git := &localGit{hasRemote: true, branch: "main"}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: "https://github.test/pr/8"}}
	p := localTestPipeline(t, git, gh, &epicTracker{})
	const branch = "feature/COD-96403-thing"
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if len(git.checkedOut) == 0 || git.checkedOut[0] != branch {
		t.Errorf("checkedOut = %v, want the recorded branch reclaimed first", git.checkedOut)
	}
	if len(git.pushedRefs) != 1 || git.pushedRefs[0] != branch {
		t.Errorf("pushedRefs = %v, want exactly [%q]", git.pushedRefs, branch)
	}
	if git.commits != 1 {
		t.Errorf("commits = %d, want 1", git.commits)
	}
}

// A reclaim that fails aborts the phase before anything is committed or pushed:
// committing wherever HEAD happens to sit is how a slice ends up on main.
func TestCommitAndPRRefusesToCommitOffBranch(t *testing.T) {
	id := "COD-96404"
	git := &localGit{hasRemote: true, branch: "main", checkoutErr: errors.New("local changes would be overwritten")}
	gh := &countingGitHub{}
	p := localTestPipeline(t, git, gh, &epicTracker{})
	if err := p.State.Set(id, "BRANCH", "feature/COD-96404-thing"); err != nil {
		t.Fatal(err)
	}

	err := p.CommitAndPR(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "moved off feature/COD-96404-thing") {
		t.Fatalf("CommitAndPR = %v, want a moved-off-branch error", err)
	}
	if git.commits != 0 {
		t.Errorf("committed %d time(s) on the wrong branch, want 0", git.commits)
	}
	if git.pushes != 0 {
		t.Errorf("pushed %d time(s), want 0", git.pushes)
	}
	if gh.touched != 0 {
		t.Errorf("made %d gh call(s), want 0", gh.touched)
	}
}

// A worktree already on the recorded branch commits without any extra checkout:
// the guard is a repair for interference, not a new step in the happy path.
func TestCommitAndPRSkipsReclaimOnRecordedBranch(t *testing.T) {
	id := "COD-96405"
	const branch = "feature/COD-96405-thing"
	git := &localGit{hasRemote: true, branch: branch}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: "https://github.test/pr/9"}}
	p := localTestPipeline(t, git, gh, &epicTracker{})
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if len(git.checkedOut) != 0 {
		t.Errorf("checkedOut = %v, want no checkout when already on the branch", git.checkedOut)
	}
	if len(git.pushedRefs) != 1 || git.pushedRefs[0] != branch {
		t.Errorf("pushedRefs = %v, want exactly [%q]", git.pushedRefs, branch)
	}
}
