package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

const (
	requeueReady      = "ready-for-agent"
	requeueQuarantine = "needs-human"
)

// requeueTracker answers with the ticket's labels and status and applies a Reset
// to them, so a second requeue sees the restored ticket rather than the
// quarantined one.
type requeueTracker struct {
	resetTracker
	labels []string
	status tracker.IssueStatus
}

func (t *requeueTracker) IssueDetail(context.Context, string) (tracker.IssueDetail, error) {
	return tracker.IssueDetail{Labels: t.labels}, nil
}

func (t *requeueTracker) IssueStatus(context.Context, string) (tracker.IssueStatus, error) {
	return t.status, nil
}

func (t *requeueTracker) Reset(ctx context.Context, id string) error {
	t.labels, t.status = []string{requeueReady}, tracker.StatusOpen
	return t.resetTracker.Reset(ctx, id)
}

// requeueGit stands in for a repo that still holds the attempt branch on both
// sides, and drops each side as it is deleted.
type requeueGit struct {
	fakeGit
	local       bool
	pushed      bool
	deleteErr   error
	checkedOut  string
	deleted     string
	deletedPush string
}

func (g *requeueGit) BranchExists(context.Context, string) (bool, error) { return g.local, nil }

func (g *requeueGit) RemoteBranchExists(context.Context, string, string) (bool, error) {
	return g.pushed, nil
}

func (g *requeueGit) Checkout(_ context.Context, branch string, _ bool) error {
	g.checkedOut = branch
	return nil
}

func (g *requeueGit) DeleteBranch(_ context.Context, branch string) error {
	if g.deleteErr != nil {
		return g.deleteErr
	}
	g.deleted, g.local = branch, false
	return nil
}

func (g *requeueGit) DeletePushedBranch(_ context.Context, remote, branch string) error {
	g.deletedPush, g.pushed = remote+"/"+branch, false
	return nil
}

func newRequeuePipeline(t *testing.T, tr tracker.Tracker, g Git, gh GitHub) (*Pipeline, *logRenderer) {
	t.Helper()
	p := newTestPipeline(t, fakeRunner{}, tr)
	rend := &logRenderer{}
	p.Git, p.GitHub, p.Renderer = g, gh, rend
	p.Remote = "origin"
	p.ReadyLabel, p.QuarantineLabel = requeueReady, requeueQuarantine
	return p, rend
}

func seedQuarantine(t *testing.T, p *Pipeline, id, branch, pr string) {
	t.Helper()
	for key, value := range map[string]string{"PHASE": state.Quarantined, "BRANCH": branch, "PR": pr} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
}

// TestRequeueRecoversAQuarantinedTicketInOneCall pins the whole five-step recovery
// on a single invocation — tracker labels and status, checkpoint, attempt PR,
// attempt branch on both sides — and the report that names each step. The second
// call is the idempotence half: with nothing left to undo it must touch neither the
// tracker nor the forge, and say so.
func TestRequeueRecoversAQuarantinedTicketInOneCall(t *testing.T) {
	id := "COD-1200"
	branch := "feature/COD-1200-ci-gate"
	tr := &requeueTracker{labels: []string{requeueQuarantine}, status: tracker.StatusStarted}
	gh := &epicGitHub{prState: "OPEN"}
	g := &requeueGit{local: true, pushed: true}
	p, rend := newRequeuePipeline(t, tr, g, gh)
	seedQuarantine(t, p, id, branch, "440")

	if err := p.Requeue(context.Background(), id, false); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	if tr.resetCalls != 1 {
		t.Errorf("tracker Reset calls = %d, want 1", tr.resetCalls)
	}
	if gh.closedPR != "440" {
		t.Errorf("closed PR %q, want 440 — the saved attempt PR stays open otherwise", gh.closedPR)
	}
	if g.checkedOut != "main" {
		t.Errorf("checked out %q, want the base branch main before the branch is dropped", g.checkedOut)
	}
	if g.deleted != branch {
		t.Errorf("deleted local branch %q, want %q", g.deleted, branch)
	}
	if want := "origin/" + branch; g.deletedPush != want {
		t.Errorf("deleted pushed branch %q, want %q", g.deletedPush, want)
	}
	if phase := p.State.Get(id, "PHASE"); phase != "" {
		t.Errorf("PHASE = %q, want the checkpoint cleared", phase)
	}
	for _, want := range []string{
		"dropped " + requeueQuarantine,
		"restored " + requeueReady,
		"moved back to To Do",
		"closed the attempt PR 440",
		"deleted branch " + branch,
		"deleted origin/" + branch,
		"cleared the saved checkpoint (was " + state.Quarantined + ")",
		id + " is eligible again",
	} {
		if !rend.contains(want) {
			t.Errorf("report is missing %q, got %v", want, rend.lines)
		}
	}

	if err := p.Requeue(context.Background(), id, false); err != nil {
		t.Fatalf("second Requeue: %v", err)
	}

	if tr.resetCalls != 1 {
		t.Errorf("tracker Reset calls = %d after a second requeue, want 1 — an already-ready ticket is left alone", tr.resetCalls)
	}
	if !rend.contains(id + " is already queued — nothing to do") {
		t.Errorf("second requeue said %v, want the no-op message", rend.lines)
	}
}

// TestRequeueRefusesAMergedAttempt: a ticket whose attempt PR already merged is
// shipped work, so the recovery refuses it untouched — and --force still gets
// through, leaving the merged PR itself alone.
func TestRequeueRefusesAMergedAttempt(t *testing.T) {
	id := "COD-1201"
	branch := "feature/COD-1201-shipped"
	tr := &requeueTracker{labels: []string{requeueQuarantine}, status: tracker.StatusStarted}
	gh := &epicGitHub{prState: "MERGED"}
	g := &requeueGit{local: true, pushed: true}
	p, _ := newRequeuePipeline(t, tr, g, gh)
	seedQuarantine(t, p, id, branch, "441")

	err := p.Requeue(context.Background(), id, false)

	if err == nil {
		t.Fatal("Requeue err = nil, want a refusal for an already-merged attempt")
	}
	if tr.resetCalls != 0 {
		t.Errorf("tracker Reset calls = %d on a refusal, want 0", tr.resetCalls)
	}
	if g.deleted != "" {
		t.Errorf("deleted branch %q on a refusal, want nothing touched", g.deleted)
	}
	if phase := p.State.Get(id, "PHASE"); phase != state.Quarantined {
		t.Errorf("PHASE = %q on a refusal, want the checkpoint kept", phase)
	}

	if err := p.Requeue(context.Background(), id, true); err != nil {
		t.Fatalf("forced Requeue: %v", err)
	}

	if gh.closedPR != "" {
		t.Errorf("closed PR %q, want the merged PR left alone", gh.closedPR)
	}
	if tr.resetCalls != 1 {
		t.Errorf("tracker Reset calls = %d under --force, want 1", tr.resetCalls)
	}
	if phase := p.State.Get(id, "PHASE"); phase != "" {
		t.Errorf("PHASE = %q under --force, want the checkpoint cleared", phase)
	}
}

// TestRequeuePartialFailureKeepsTheCheckpoint covers the step that would not go —
// a branch the working tree or the forge refuses to drop. The run stays failed,
// checkpoint and all, so the retry has something left to drive and finishes what
// the first call could not.
func TestRequeuePartialFailureKeepsTheCheckpoint(t *testing.T) {
	id := "COD-1203"
	branch := "feature/COD-1203-stuck"
	tr := &requeueTracker{labels: []string{requeueQuarantine}, status: tracker.StatusStarted}
	g := &requeueGit{local: true, deleteErr: errors.New("permission denied")}
	p, _ := newRequeuePipeline(t, tr, g, &epicGitHub{})
	seedQuarantine(t, p, id, branch, "")

	if err := p.Requeue(context.Background(), id, false); err == nil {
		t.Fatal("Requeue err = nil, want the branch delete to fail")
	}
	if phase := p.State.Get(id, "PHASE"); phase != state.Quarantined {
		t.Fatalf("PHASE = %q after a partial recovery, want the checkpoint kept for the retry", phase)
	}

	g.deleteErr = nil
	if err := p.Requeue(context.Background(), id, false); err != nil {
		t.Fatalf("retry Requeue: %v", err)
	}

	if g.deleted != branch {
		t.Errorf("deleted branch %q on the retry, want %q", g.deleted, branch)
	}
	if phase := p.State.Get(id, "PHASE"); phase != "" {
		t.Errorf("PHASE = %q after the retry, want the checkpoint cleared", phase)
	}
}

// TestRequeueRestoresBlindWithoutTrackerReads covers a tracker that cannot report
// labels or status (GitHub issues): the restore still runs — it is idempotent —
// and the report says what it did without claiming a diff it never read.
func TestRequeueRestoresBlindWithoutTrackerReads(t *testing.T) {
	id := "COD-1202"
	tr := &resetTracker{}
	p, rend := newRequeuePipeline(t, tr, &requeueGit{}, &epicGitHub{})

	if err := p.Requeue(context.Background(), id, false); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	if tr.resetCalls != 1 {
		t.Errorf("tracker Reset calls = %d, want 1 — a tracker that cannot be read is restored anyway", tr.resetCalls)
	}
	if !rend.contains("restored the tracker labels and status") {
		t.Errorf("report = %v, want the blind-restore line", rend.lines)
	}
}
