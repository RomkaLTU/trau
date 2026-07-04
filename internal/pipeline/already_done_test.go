package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestResumeSkipsMergedCheckpoint is the COD-708 regression guard: a ticket
// whose checkpoint already says merged (delivered by a previous run) must
// short-circuit to ErrAlreadyDone before touching git or the checkpoint —
// a bad pick of a merged ticket used to re-enter build, clobber the merged
// phase, and fault on the merge-deleted branch.
func TestResumeSkipsMergedCheckpoint(t *testing.T) {
	id := "COD-90708"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	if err := p.State.Set(id, "PHASE", state.Merged); err != nil {
		t.Fatal(err)
	}
	if err := p.State.Set(id, "PR", "86"); err != nil {
		t.Fatal(err)
	}

	err := p.Resume(context.Background(), id, "")
	if !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("expected ErrAlreadyDone, got %v", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Fatalf("merged checkpoint must stay untouched, got PHASE=%q", got)
	}
}

// missingBranchGit simulates a recorded feature branch that no longer exists
// locally (fakeGit.BranchExists is false), with a configurable remote copy.
type missingBranchGit struct {
	fakeGit
	remote  bool
	created string
	adopted string
}

func (g *missingBranchGit) RemoteBranchExists(context.Context, string, string) (bool, error) {
	return g.remote, nil
}

func (g *missingBranchGit) CheckoutRemoteBranch(_ context.Context, _, branch string) error {
	g.adopted = branch
	return nil
}

func (g *missingBranchGit) CreateBranch(_ context.Context, branch, _ string) error {
	g.created = branch
	return nil
}

// TestResolveBuildBranchWithMissingRecordedBranch covers the two recoveries
// for a recorded-but-gone branch (COD-708): adopt the remote copy when one
// exists, otherwise start fresh — never fault on the checkout.
func TestResolveBuildBranchWithMissingRecordedBranch(t *testing.T) {
	tests := []struct {
		name        string
		remote      bool
		wantAdopted string
		wantCreated string
	}{
		{name: "adopts remote branch", remote: true, wantAdopted: "feature/COD-90709-old"},
		{name: "recreates fresh branch", remote: false, wantCreated: "feature/COD-90709"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "COD-90709"
			git := &missingBranchGit{remote: tt.remote}
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Git = git
			if err := p.State.Set(id, "BRANCH", "feature/COD-90709-old"); err != nil {
				t.Fatal(err)
			}

			branch, err := p.resolveBuildBranch(context.Background(), id)
			if err != nil {
				t.Fatalf("resolveBuildBranch returned error: %v", err)
			}
			if git.adopted != tt.wantAdopted {
				t.Errorf("adopted = %q, want %q", git.adopted, tt.wantAdopted)
			}
			if git.created != tt.wantCreated {
				t.Errorf("created = %q, want %q", git.created, tt.wantCreated)
			}
			want := tt.wantAdopted
			if want == "" {
				want = tt.wantCreated
			}
			if branch != want {
				t.Errorf("branch = %q, want %q", branch, want)
			}
		})
	}
}
