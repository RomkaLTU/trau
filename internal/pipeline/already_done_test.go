package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
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

// statusTracker extends fakeTracker with a canned IssueStatus answer.
type statusTracker struct {
	fakeTracker
	status    tracker.IssueStatus
	statusErr error
}

func (t *statusTracker) IssueStatus(context.Context, string) (tracker.IssueStatus, error) {
	return t.status, t.statusErr
}

// doneFailTracker simulates a swallowed mark-done failure (e.g. a transient 429).
type doneFailTracker struct{ fakeTracker }

func (t *doneFailTracker) SetStatus(context.Context, string, string, string) error {
	return errors.New("429 rate limited")
}

// TestResumeRebuildsReopenedTicket is the COD-747 guard: a merged checkpoint
// whose ticket trau marked Done and a human then reopened in the tracker must
// clear the delivered checkpoint and rebuild instead of skipping.
func TestResumeRebuildsReopenedTicket(t *testing.T) {
	id := "COD-90747"
	p := newTestPipeline(t, fakeRunner{err: errors.New("boom")}, &statusTracker{status: tracker.StatusOpen})
	for k, v := range map[string]string{"PHASE": state.Merged, "PR": "90", "TRACKER_DONE": "1"} {
		if err := p.State.Set(id, k, v); err != nil {
			t.Fatal(err)
		}
	}

	err := p.Resume(context.Background(), id, "")
	if errors.Is(err, ErrAlreadyDone) {
		t.Fatal("reopened ticket must rebuild, got ErrAlreadyDone")
	}
	if got := p.State.Get(id, "PHASE"); got == state.Merged {
		t.Fatalf("merged checkpoint must be cleared, got PHASE=%q", got)
	}
}

// TestResumeKeepsMergedSkipUnlessAffirmativelyReopened locks the fail-safe side
// of COD-747: no TRACKER_DONE marker (the Done write may have failed), a status
// lookup error, or a still-terminal status must all keep today's skip.
func TestResumeKeepsMergedSkipUnlessAffirmativelyReopened(t *testing.T) {
	tests := []struct {
		name   string
		marker bool
		status tracker.IssueStatus
		err    error
	}{
		{name: "no marker after failed mark-done", marker: false, status: tracker.StatusOpen},
		{name: "still done in tracker", marker: true, status: tracker.StatusDone},
		{name: "canceled in tracker", marker: true, status: tracker.StatusCanceled},
		{name: "unknown status", marker: true, status: tracker.StatusUnknown},
		{name: "status lookup error", marker: true, status: tracker.StatusOpen, err: errors.New("tracker down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "COD-90748"
			p := newTestPipeline(t, fakeRunner{}, &statusTracker{status: tt.status, statusErr: tt.err})
			if err := p.State.Set(id, "PHASE", state.Merged); err != nil {
				t.Fatal(err)
			}
			if tt.marker {
				if err := p.State.Set(id, "TRACKER_DONE", "1"); err != nil {
					t.Fatal(err)
				}
			}

			if err := p.Resume(context.Background(), id, ""); !errors.Is(err, ErrAlreadyDone) {
				t.Fatalf("expected ErrAlreadyDone, got %v", err)
			}
			if got := p.State.Get(id, "PHASE"); got != state.Merged {
				t.Fatalf("merged checkpoint must stay untouched, got PHASE=%q", got)
			}
		})
	}
}

// TestMarkDoneRecordsTrackerDone: the marker is written only when the tracker
// positively reached Done, so a swallowed SetStatus failure can never later
// read as a human reopen.
func TestMarkDoneRecordsTrackerDone(t *testing.T) {
	t.Run("marker on success", func(t *testing.T) {
		id := "COD-90749"
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		if err := p.markDone(context.Background(), id, "merged %s"); err != nil {
			t.Fatal(err)
		}
		if got := p.State.Get(id, "TRACKER_DONE"); got != "1" {
			t.Fatalf("TRACKER_DONE = %q, want \"1\"", got)
		}
	})
	t.Run("no marker when SetStatus fails", func(t *testing.T) {
		id := "COD-90750"
		p := newTestPipeline(t, fakeRunner{}, &doneFailTracker{})
		if err := p.markDone(context.Background(), id, "merged %s"); err != nil {
			t.Fatal(err)
		}
		if got := p.State.Get(id, "TRACKER_DONE"); got != "" {
			t.Fatalf("TRACKER_DONE = %q, want unset", got)
		}
		if got := p.State.Get(id, "PHASE"); got != state.Merged {
			t.Fatalf("PHASE = %q, want merged despite tracker failure", got)
		}
	})
}
