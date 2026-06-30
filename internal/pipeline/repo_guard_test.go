package pipeline

import (
	"context"
	"strings"
	"testing"
)

// guardGit is a fakeGit whose worktree dirtiness and branch-ahead commits are
// fixed per case, so assertRepoChanged can be exercised without a real repo.
type guardGit struct {
	fakeGit
	dirty   bool
	commits []string
}

func (g guardGit) WorktreeDirty(context.Context) (bool, error)               { return g.dirty, nil }
func (g guardGit) Commits(context.Context, string, string) ([]string, error) { return g.commits, nil }

// TestAssertRepoChanged is the COD-633 guard: after build, a managed repo with a
// clean tree AND no commits beyond base means the agent built nothing here (likely
// a wrong-repo / cwd-escape) and must fault rather than advance to a hollow
// handoff — while a real diff, committed work, or REQUIRE_REPO_CHANGES=0 all pass.
func TestAssertRepoChanged(t *testing.T) {
	cases := []struct {
		name     string
		require  bool
		dirty    bool
		commits  []string
		wantFire bool
	}{
		{name: "clean tree, no commits, guard on", require: true, wantFire: true},
		{name: "real diff in tree", require: true, dirty: true},
		{name: "committed work, clean tree", require: true, commits: []string{"abc123"}},
		{name: "guard disabled", require: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.RequireRepoChanges = tc.require
			p.Git = guardGit{dirty: tc.dirty, commits: tc.commits}
			if err := p.State.Set("COD-633", "BRANCH", "feature/COD-633-x"); err != nil {
				t.Fatal(err)
			}

			err := p.assertRepoChanged(context.Background(), "COD-633")

			if !tc.wantFire {
				if err != nil {
					t.Fatalf("guard fired unexpectedly: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("guard did not fire on a clean, commit-less repo")
			}
			// It must route to fault (resumable), never give-up (quarantine).
			if isGiveUp(err) {
				t.Errorf("guard error must not be a *GiveUpError (would quarantine): %v", err)
			}
			if !strings.Contains(err.Error(), "wrong repository") {
				t.Errorf("guard message = %q, want it to name the wrong-repo cause", err)
			}
		})
	}
}
