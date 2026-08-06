package pipeline

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// parkedGit leaves HEAD on the feature branch a run outside trau left checked out.
type parkedGit struct {
	fakeGit
	branch string
}

func (g parkedGit) CurrentBranch(context.Context) (string, error) { return g.branch, nil }

// inferredTracker answers the ticket's lifecycle status and records reconcile writes.
type inferredTracker struct {
	fakeTracker
	status   tracker.IssueStatus
	statuses []tracker.Stage
}

func (t *inferredTracker) IssueStatus(context.Context, string) (tracker.IssueStatus, error) {
	return t.status, nil
}

func (t *inferredTracker) SetStatus(_ context.Context, _ string, stage tracker.Stage, _ string) error {
	t.statuses = append(t.statuses, stage)
	return nil
}

func newParkedPipeline(t *testing.T, branch string, gh *fakeGitHub, tr tracker.Tracker) (*Pipeline, *logRenderer) {
	t.Helper()
	logs := &logRenderer{}
	p := newTestPipeline(t, fakeRunner{}, tr)
	p.Git = parkedGit{branch: branch}
	p.Delivery = gh
	p.Renderer = logs
	return p, logs
}

// writePassingVerdict lays down the artifact that would otherwise infer a verified
// checkpoint.
func writePassingVerdict(t *testing.T, id string) {
	t.Helper()
	if err := os.WriteFile(verifyPath(id), []byte(`{"pass":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A branch whose PR was merged outside trau must never be adopted and re-run through
// handoff, verify and the CI gate.
func TestInferredResumeDeclinesBranchWithMergedPR(t *testing.T) {
	const (
		id     = "COD-91242"
		branch = "feature/COD-91242-import-run-sheets"
		prURL  = "https://github.com/o/r/pull/215"
	)
	writeHandoff(t, id)
	writePassingVerdict(t, id)
	tr := &inferredTracker{}
	p, logs := newParkedPipeline(t, branch, &fakeGitHub{mergedURL: prURL}, tr)

	gotID, gotPhase := p.InferredResumeFunc(context.Background(), nil)

	if gotID != "" || gotPhase != "" {
		t.Fatalf("InferredResumeFunc = (%q, %q), want no adoption of a delivered branch", gotID, gotPhase)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want the checkpoint settled at %q", got, state.Merged)
	}
	if got := p.State.Get(id, "PR"); got != "215" {
		t.Errorf("PR = %q, want 215", got)
	}
	if got := p.State.Get(id, "PR_URL"); got != prURL {
		t.Errorf("PR_URL = %q, want %q", got, prURL)
	}
	if got := p.State.Get(id, "BRANCH"); got != branch {
		t.Errorf("BRANCH = %q, want %q", got, branch)
	}
	if len(tr.statuses) != 1 || tr.statuses[0] != tracker.StageDone {
		t.Errorf("tracker statuses = %v, want [done]", tr.statuses)
	}
	if !logs.contains("already delivered via PR #215") {
		t.Errorf("log lines %v, want one naming the merged PR", logs.lines)
	}

	calls := &promptLog{}
	p.Runner = fakeRunner{calls: calls}
	if err := p.Resume(context.Background(), id, ""); !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("Resume err = %v, want ErrAlreadyDone", err)
	}
	if n := len(calls.all()); n != 0 {
		t.Errorf("%d agent calls on delivered work, want 0", n)
	}
}

func TestInferredResumeAdoptsWhenNothingWasDelivered(t *testing.T) {
	cases := []struct {
		name      string
		id        string
		prURL     string
		handoff   bool
		wantPhase string
	}{
		{name: "no PR", id: "COD-91243", wantPhase: state.Built},
		{name: "no PR with a handoff on disk", id: "COD-91244", handoff: true, wantPhase: state.HandedOff},
		{name: "open PR", id: "COD-91245", prURL: "https://github.com/o/r/pull/7", wantPhase: state.PROpen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			branch := "feature/" + tc.id + "-slice"
			if tc.handoff {
				writeHandoff(t, tc.id)
			}
			p, logs := newParkedPipeline(t, branch, &fakeGitHub{prURL: tc.prURL}, &inferredTracker{})

			gotID, gotPhase := p.InferredResumeFunc(context.Background(), nil)

			if gotID != tc.id || gotPhase != tc.wantPhase {
				t.Fatalf("InferredResumeFunc = (%q, %q), want (%q, %q)", gotID, gotPhase, tc.id, tc.wantPhase)
			}
			if got := p.State.Get(tc.id, "PHASE"); got != tc.wantPhase {
				t.Errorf("PHASE = %q, want %q", got, tc.wantPhase)
			}
			if got := p.State.Get(tc.id, "PR_URL"); got != tc.prURL {
				t.Errorf("PR_URL = %q, want %q", got, tc.prURL)
			}
			if !logs.contains("adopted in-progress branch " + branch) {
				t.Errorf("log lines %v, want the adoption line naming %s", logs.lines, branch)
			}
		})
	}
}

// A run pinned to one ticket leaves another ticket's parked branch alone, and says so
// rather than silently skipping it.
func TestInferredResumeFuncIgnoresBranchOutsideScope(t *testing.T) {
	const (
		forced = "COD-91247"
		id     = "COD-91248"
		branch = "feature/COD-91248-stray"
	)
	p, logs := newParkedPipeline(t, branch, &fakeGitHub{}, &inferredTracker{})

	keep := func(got string) bool { return got == forced }
	gotID, gotPhase := p.InferredResumeFunc(context.Background(), keep)

	if gotID != "" || gotPhase != "" {
		t.Fatalf("InferredResumeFunc = (%q, %q), want no adoption outside the run's scope", gotID, gotPhase)
	}
	if got := p.State.Get(id, "PHASE"); got != "" {
		t.Errorf("PHASE = %q, want no checkpoint written", got)
	}
	if got := p.State.Get(id, "BRANCH"); got != "" {
		t.Errorf("BRANCH = %q, want no checkpoint written", got)
	}
	if !logs.contains("parked branch " + branch + " ignored") {
		t.Errorf("log lines %v, want one naming the skipped branch", logs.lines)
	}
}

// The pinned ticket's own parked branch is still adopted.
func TestInferredResumeFuncAdoptsBranchInScope(t *testing.T) {
	const (
		id     = "COD-91249"
		branch = "feature/COD-91249-pinned"
	)
	p, logs := newParkedPipeline(t, branch, &fakeGitHub{}, &inferredTracker{})

	keep := func(got string) bool { return got == id }
	gotID, gotPhase := p.InferredResumeFunc(context.Background(), keep)

	if gotID != id || gotPhase != state.Built {
		t.Fatalf("InferredResumeFunc = (%q, %q), want (%q, %q)", gotID, gotPhase, id, state.Built)
	}
	if !logs.contains("adopted in-progress branch " + branch) {
		t.Errorf("log lines %v, want the adoption line naming %s", logs.lines, branch)
	}
}

// An Azure DevOps board settles on no prefix, so the parked branch leads with the bare
// work-item number and the resume path has to recognise it there.
func TestInferredResumeFuncAdoptsABareNumberedBranch(t *testing.T) {
	p, _ := newParkedPipeline(t, "feature/6694-bare-numeric-ids", &fakeGitHub{}, &inferredTracker{})
	p.Prefix = ""
	p.TrackerProvider = "azure"

	gotID, gotPhase := p.InferredResumeFunc(context.Background(), nil)

	if gotID != "6694" || gotPhase != state.Built {
		t.Fatalf("InferredResumeFunc = (%q, %q), want (6694, %q)", gotID, gotPhase, state.Built)
	}
}

// With no PR to prove delivery, a tracker-terminal ticket declines adoption but leaves
// the checkpoint untouched.
func TestInferredResumeDeclinesTicketFinishedInTracker(t *testing.T) {
	const (
		id     = "COD-91246"
		branch = "feature/COD-91246-shipped"
	)
	tr := &inferredTracker{status: tracker.StatusDone}
	p, logs := newParkedPipeline(t, branch, &fakeGitHub{}, tr)

	gotID, gotPhase := p.InferredResumeFunc(context.Background(), nil)

	if gotID != "" || gotPhase != "" {
		t.Fatalf("InferredResumeFunc = (%q, %q), want no adoption of a finished ticket", gotID, gotPhase)
	}
	if got := p.State.Get(id, "PHASE"); got != "" {
		t.Errorf("PHASE = %q, want no checkpoint written", got)
	}
	if len(tr.statuses) != 0 {
		t.Errorf("tracker statuses = %v, want none written", tr.statuses)
	}
	if !logs.contains("reads done in the tracker") {
		t.Errorf("log lines %v, want one naming the tracker status", logs.lines)
	}
}
