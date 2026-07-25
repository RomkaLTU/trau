package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// ciGitHub answers the branch → PR lookups the CI gate reconciles with and records
// what it gated, so a test can assert a PR-less checkpoint never polled CI for "".
type ciGitHub struct {
	fakeGitHub
	checks   []Check
	polled   []string
	mergedPR string
}

func (g *ciGitHub) Checks(_ context.Context, pr string) ([]Check, error) {
	g.polled = append(g.polled, pr)
	return g.checks, nil
}

func (g *ciGitHub) Merge(_ context.Context, pr, _ string, _ bool) error {
	g.mergedPR = pr
	return nil
}

func newGatePipeline(t *testing.T, gh GitHub, tr tracker.Tracker) *Pipeline {
	t.Helper()
	p := newTestPipeline(t, fakeRunner{}, tr)
	p.GitHub = gh
	p.AutoMerge = true
	p.MergeMethod = "squash"
	p.RequireCI = true
	p.Sleep = func(time.Duration) {}
	return p
}

// A checkpoint whose PR field is empty but whose branch was already merged must be
// reconciled from the branch and marked Done — never gated, since PRState("") can
// never report MERGED and pollCI("") quarantines delivered work as "CI not green".
func TestCIAndMergeReconcilesEmptyPRFromMergedBranch(t *testing.T) {
	const (
		id     = "COD-91144"
		branch = "feature/COD-91144-fleet-index-deferred-tables"
		prURL  = "https://github.com/o/r/pull/215"
	)
	gh := &ciGitHub{}
	gh.mergedURL = prURL
	tr := &inferredTracker{}
	p := newGatePipeline(t, gh, tr)
	mustSet(t, p, id, "BRANCH", branch)
	mustSet(t, p, id, "PHASE", state.HandedOff)

	if err := p.CIAndMerge(context.Background(), id); !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("CIAndMerge = %v, want ErrAlreadyDone", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want %q", got, state.Merged)
	}
	if got := p.State.Get(id, "PR"); got != "215" {
		t.Errorf("PR = %q, want 215", got)
	}
	if got := p.State.Get(id, "PR_URL"); got != prURL {
		t.Errorf("PR_URL = %q, want %q", got, prURL)
	}
	if len(tr.statuses) != 1 || tr.statuses[0] != "Done" {
		t.Errorf("tracker statuses = %v, want [Done]", tr.statuses)
	}
	if len(gh.polled) != 0 {
		t.Errorf("CI polled for %v, want no poll on delivered work", gh.polled)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times on delivered work, want 0", tr.quarantineCalls)
	}
}

// The open-PR side: the branch's PR is adopted onto the checkpoint and the normal
// CI gate runs on it.
func TestCIAndMergeAdoptsOpenPRForEmptyPRField(t *testing.T) {
	const (
		id     = "COD-91145"
		branch = "feature/COD-91145-fleet-index"
		prURL  = "https://github.com/o/r/pull/216"
	)
	gh := &ciGitHub{checks: []Check{{Name: "ci/test", Bucket: "pass"}}}
	gh.prURL = prURL
	p := newGatePipeline(t, gh, &inferredTracker{})
	mustSet(t, p, id, "BRANCH", branch)
	mustSet(t, p, id, "PHASE", state.PROpen)

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if got := p.State.Get(id, "PR"); got != "216" {
		t.Errorf("PR = %q, want 216", got)
	}
	if got := p.State.Get(id, "PR_URL"); got != prURL {
		t.Errorf("PR_URL = %q, want %q", got, prURL)
	}
	if len(gh.polled) != 1 || gh.polled[0] != "216" {
		t.Errorf("CI polled for %v, want [216]", gh.polled)
	}
	if gh.mergedPR != "216" {
		t.Errorf("merged PR = %q, want 216", gh.mergedPR)
	}
}

// With no PR anywhere the give-up must name the missing PR, not misattribute the
// failure to red CI — and it must not poll CI to get there.
func TestCIAndMergeGivesUpWhenBranchHasNoPR(t *testing.T) {
	const (
		id     = "COD-91146"
		branch = "feature/COD-91146-fleet-index"
	)
	gh := &ciGitHub{}
	p := newGatePipeline(t, gh, &inferredTracker{})
	mustSet(t, p, id, "BRANCH", branch)
	mustSet(t, p, id, "PHASE", state.PROpen)

	err := p.CIAndMerge(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if want := "no PR recorded or found for branch " + branch; giveUp.Reason != want {
		t.Errorf("give-up reason = %q, want %q", giveUp.Reason, want)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want %q", got, state.Quarantined)
	}
	if len(gh.polled) != 0 {
		t.Errorf("CI polled for %v, want no poll without a PR", gh.polled)
	}
}

// A post-handoff resume of a PR-less checkpoint whose branch already merged settles
// before any phase runs, so the delivered ticket costs no agent call.
func TestResumeShortCircuitsPRLessCheckpointOnMergedBranch(t *testing.T) {
	const (
		id     = "COD-91147"
		branch = "feature/COD-91147-fleet-index"
		prURL  = "https://github.com/o/r/pull/217"
	)
	gh := &ciGitHub{}
	gh.mergedURL = prURL
	tr := &inferredTracker{}
	p := newGatePipeline(t, gh, tr)
	calls := &promptLog{}
	p.Runner = fakeRunner{calls: calls}
	mustSet(t, p, id, "BRANCH", branch)
	mustSet(t, p, id, "PHASE", state.HandedOff)
	mustSet(t, p, id, "FAILURE_REASON", "CI not green")

	if err := p.Resume(context.Background(), id, state.HandedOff); !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("Resume = %v, want ErrAlreadyDone", err)
	}
	if n := len(calls.all()); n != 0 {
		t.Errorf("%d agent calls on delivered work, want 0", n)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want %q", got, state.Merged)
	}
	if got := p.State.Get(id, "PR"); got != "217" {
		t.Errorf("PR = %q, want 217", got)
	}
	if got := p.State.Get(id, "FAILURE_REASON"); got != "" {
		t.Errorf("FAILURE_REASON = %q, want cleared", got)
	}
	if len(tr.statuses) != 1 || tr.statuses[0] != "Done" {
		t.Errorf("tracker statuses = %v, want [Done]", tr.statuses)
	}
}

// The reconcile stays out of the way of everything else: a pre-handoff resume, a
// checkpoint that still has its PR, and a branch with no merged PR all run their
// phases as before.
func TestResumeReconcileLeavesOtherCheckpointsAlone(t *testing.T) {
	cases := []struct {
		name      string
		from      string
		pr        string
		mergedURL string
	}{
		{name: "pre-handoff resume", from: state.Built, mergedURL: "https://github.com/o/r/pull/218"},
		{name: "checkpoint still has its PR", from: state.PROpen, pr: "219"},
		{name: "branch never shipped", from: state.Verified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "COD-91148"
			gh := &ciGitHub{}
			gh.mergedURL = tc.mergedURL
			p := newGatePipeline(t, gh, &inferredTracker{})
			p.Runner = fakeRunner{err: errors.New("boom")}
			mustSet(t, p, id, "BRANCH", "feature/COD-91148-fleet-index")
			mustSet(t, p, id, "PHASE", tc.from)
			if tc.pr != "" {
				mustSet(t, p, id, "PR", tc.pr)
			}

			if err := p.Resume(context.Background(), id, tc.from); errors.Is(err, ErrAlreadyDone) {
				t.Fatal("Resume = ErrAlreadyDone, want the phases to run")
			}
			if got := p.State.Get(id, "PHASE"); got == state.Merged {
				t.Error("PHASE = merged, want the checkpoint left alone")
			}
		})
	}
}

// A checkpoint with neither a PR nor a branch has nothing to reconcile from and
// must say so.
func TestResolvePRWithoutBranch(t *testing.T) {
	const id = "COD-91149"
	p := newGatePipeline(t, &ciGitHub{}, &inferredTracker{})
	mustSet(t, p, id, "PHASE", state.PROpen)

	_, err := p.resolvePR(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("resolvePR = %v, want a *GiveUpError", err)
	}
	if !strings.Contains(giveUp.Reason, "no PR recorded") {
		t.Errorf("give-up reason = %q, want it to name the missing PR", giveUp.Reason)
	}
}
