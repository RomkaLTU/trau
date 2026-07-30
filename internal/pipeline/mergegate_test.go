package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// gateGit reports the run's own committed work — the commits above its fork point
// and the files they touch.
type gateGit struct {
	mergeGit
	commits []string
	files   int
	statErr error
}

func (g *gateGit) Commits(context.Context, string, string) ([]string, error) {
	return g.commits, nil
}

func (g *gateGit) DiffStat(context.Context, string, string) (int, int, int, error) {
	return g.files, 0, 0, g.statErr
}

const gateForkPoint = "4919ffd1c0de4919ffd1c0de4919ffd1c0de4919"

// A PR carrying commits this run never pushed is diffed against a base older than
// the branch point: auto-merging it buries that base's drift as duplicated content
// It is parked for a human instead: quarantined, PR left open, nothing merged.
func TestCIAndMergeRefusesPRWithCommitsTheRunNeverPushed(t *testing.T) {
	id := "COD-90412"
	git := &gateGit{mergeGit: mergeGit{branch: "feature/COD-90412-x"}, commits: []string{"c0ffee"}, files: 47}
	gh := &mergeGitHub{epicGitHub: epicGitHub{prCommits: 13, prFiles: 470}}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	seedGatedPR(t, p, id, git.branch)

	err := p.CIAndMerge(context.Background(), id)
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if !strings.Contains(g.Reason, "13 commit(s) but this run pushed 1") {
		t.Errorf("give-up reason = %q, want it to contrast the PR's commits with the run's", g.Reason)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0", gh.mergeCalls)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine called %d times, want 1 (parked for a human)", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want quarantined", got)
	}
}

// Same commit count, wildly wider diff: the PR's base is missing the fork point in a
// way the commit count alone cannot see, so the file counts catch it.
func TestCIAndMergeRefusesPRWhoseDiffDwarfsTheRun(t *testing.T) {
	id := "COD-90413"
	git := &gateGit{mergeGit: mergeGit{branch: "feature/COD-90413-x"}, commits: []string{"c0ffee"}, files: 47}
	gh := &mergeGitHub{epicGitHub: epicGitHub{prCommits: 1, prFiles: 470}}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	seedGatedPR(t, p, id, git.branch)

	err := p.CIAndMerge(context.Background(), id)
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if !strings.Contains(g.Reason, "470 file(s) but this run touched 47") {
		t.Errorf("give-up reason = %q, want it to contrast the PR's files with the run's", g.Reason)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0", gh.mergeCalls)
	}
}

// The gate only refuses work that is not this slice. A PR matching what the run
// pushed merges, and so does one the gate cannot measure — a delivery is never
// blocked on the gate's own instrumentation.
func TestCIAndMergeMergesWhenPRMatchesTheRun(t *testing.T) {
	tests := []struct {
		name      string
		git       *gateGit
		gh        *mergeGitHub
		noForkPin bool
	}{
		{
			name: "PR is exactly the run's work",
			git:  &gateGit{commits: []string{"c0ffee"}, files: 47},
			gh:   &mergeGitHub{epicGitHub: epicGitHub{prCommits: 1, prFiles: 47}},
		},
		{
			name: "a few extra files stay under the multiple",
			git:  &gateGit{commits: []string{"c0ffee"}, files: 47},
			gh:   &mergeGitHub{epicGitHub: epicGitHub{prCommits: 1, prFiles: 60}},
		},
		{
			name: "a tiny slice is not tripped by its own multiple",
			git:  &gateGit{commits: []string{"c0ffee"}, files: 1},
			gh:   &mergeGitHub{epicGitHub: epicGitHub{prCommits: 1, prFiles: 4}},
		},
		{
			name: "gh cannot size the PR",
			git:  &gateGit{commits: []string{"c0ffee"}, files: 47},
			gh:   &mergeGitHub{},
		},
		{
			name: "git cannot size the run's diff",
			git:  &gateGit{commits: []string{"c0ffee"}, statErr: errors.New("bad revision")},
			gh:   &mergeGitHub{epicGitHub: epicGitHub{prCommits: 1, prFiles: 470}},
		},
		{
			name:      "the run recorded no fork point",
			git:       &gateGit{commits: []string{"c0ffee"}, files: 47},
			gh:        &mergeGitHub{epicGitHub: epicGitHub{prCommits: 13, prFiles: 470}},
			noForkPin: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "COD-90414"
			tt.git.branch = "feature/COD-90414-x"
			tr := &fakeTracker{}
			p := newMergePipeline(t, tt.git, tt.gh, tr)
			seedPROpen(t, p, id, "412", tt.git.branch)
			if !tt.noForkPin {
				if err := p.State.Set(id, "BASE_SHA", gateForkPoint); err != nil {
					t.Fatal(err)
				}
			}

			if err := p.CIAndMerge(context.Background(), id); err != nil {
				t.Fatalf("CIAndMerge = %v, want nil", err)
			}
			if tt.gh.mergeCalls != 1 {
				t.Errorf("Merge called %d times, want 1", tt.gh.mergeCalls)
			}
			if tr.quarantineCalls != 0 {
				t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
			}
		})
	}
}

// The gate guards the auto-merge. With AUTO_MERGE=0 the operator merges the PR
// themselves and reviews what it contains, so nothing is refused on their behalf.
func TestCIAndMergeLeavesMismatchedPRToTheOperator(t *testing.T) {
	id := "COD-90415"
	gh := &waitGitHub{
		epicGitHub: epicGitHub{prCommits: 13, prFiles: 470},
		replies:    []prReply{{state: "OPEN"}, {state: "MERGED"}},
	}
	tr := &fakeTracker{}
	p := newWaitPipeline(t, gh, tr)
	p.Git = &gateGit{commits: []string{"c0ffee"}, files: 47}
	seedGatedPR(t, p, id, "feature/COD-90415-x")

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0 — the operator owns this merge", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged once the operator merged it", got)
	}
}

func seedGatedPR(t *testing.T, p *Pipeline, id, branch string) {
	t.Helper()
	seedPROpen(t, p, id, "412", branch)
	if err := p.State.Set(id, "BASE_SHA", gateForkPoint); err != nil {
		t.Fatal(err)
	}
}
