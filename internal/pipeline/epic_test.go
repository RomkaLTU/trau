package pipeline

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestFinalizeEpicAutoMergesWhenCIGreen(t *testing.T) {
	tr := &epicTracker{
		title: "Checkout rebuild",
		subs: []tracker.SubIssue{
			{ID: "COD-2", Title: "first"},
			{ID: "COD-3", Title: "second"},
		},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
			"COD-3": tracker.StatusDone,
		},
	}
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := &Pipeline{
		Base:        "main",
		Remote:      "origin",
		EpicID:      "COD-1",
		exit:        exitState{epicBranch: "epic/COD-1-checkout-rebuild"},
		AutoMerge:   true,
		RequireCI:   config.CIGateOn,
		MergeMethod: "squash",
		Git:         baseCurrentGit{},
		GitHub:      gh,
		Tracker:     tr,
		State:       state.NewStore(t.TempDir()),
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("expected one epic merge on green CI, got %d", gh.mergeCalls)
	}
	assertEpicCheckpointedMerged(t, p)
	if gh.mergeMethod != "squash" || !gh.mergeDeleted {
		t.Fatalf("expected squash merge with branch delete, got %q delete=%v", gh.mergeMethod, gh.mergeDeleted)
	}
	if tr.setStatus != tracker.StageDone || !strings.Contains(tr.setExtra, "merged to main") {
		t.Fatalf("expected epic closed as merged, got %s %q", tr.setStatus, tr.setExtra)
	}
}

// A repo whose only workflow runs on push (not pull_request) produces a PR with
// zero checks. With RequireCI off, the CI gate is bypassed so the epic still
// merges instead of spinning to ErrCITimeout. Guards the M4C-57-style quarantine.
func TestFinalizeEpicMergesWithRequireCIOffAndNoChecks(t *testing.T) {
	tr := &epicTracker{
		title: "Checkout rebuild",
		subs: []tracker.SubIssue{
			{ID: "COD-2", Title: "first"},
			{ID: "COD-3", Title: "second"},
		},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
			"COD-3": tracker.StatusDone,
		},
	}
	gh := &epicGitHub{createURL: "https://github.test/pr/42"} // no checks ever appear
	p := &Pipeline{
		Base:        "main",
		Remote:      "origin",
		EpicID:      "COD-1",
		exit:        exitState{epicBranch: "epic/COD-1-checkout-rebuild"},
		AutoMerge:   true,
		RequireCI:   config.CIGateOff,
		MergeMethod: "squash",
		Git:         baseCurrentGit{},
		GitHub:      gh,
		Tracker:     tr,
		State:       state.NewStore(t.TempDir()),
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("REQUIRE_CI=0 must merge a checkless PR, got %d merges", gh.mergeCalls)
	}
	if tr.setStatus != tracker.StageDone {
		t.Fatalf("expected epic closed Done, got %s", tr.setStatus)
	}
}

// A finalize re-attempt after the epic PR already merged must settle clean, not
// wedge the queue: the open-PR filter reads a merged epic PR as "no PR", the
// re-create is refused with "No commits between", and the merged PR is adopted
// instead of pausing the item forever (COD-1158 — epic COD-1151 re-paused on
// every Start).
func TestFinalizeEpicReattemptAdoptsMergedPR(t *testing.T) {
	tr := &epicTracker{
		title: "Checkout rebuild",
		subs: []tracker.SubIssue{
			{ID: "COD-2", Title: "first"},
			{ID: "COD-3", Title: "second"},
		},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
			"COD-3": tracker.StatusDone,
		},
	}
	gh := &epicGitHub{
		createErr: errors.New(`gh pr create: exit status 1: pull request create failed: GraphQL: No commits between main and epic/COD-1-checkout-rebuild (createPullRequest)`),
		mergedURL: "https://github.test/pr/42",
		prState:   "MERGED",
	}
	p := &Pipeline{
		Base:        "main",
		Remote:      "origin",
		EpicID:      "COD-1",
		exit:        exitState{epicBranch: "epic/COD-1-checkout-rebuild"},
		AutoMerge:   true,
		RequireCI:   config.CIGateOn,
		MergeMethod: "squash",
		Git:         baseCurrentGit{},
		GitHub:      gh,
		Tracker:     tr,
		State:       state.NewStore(t.TempDir()),
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.createCalls != 1 {
		t.Fatalf("expected one create attempt, got %d", gh.createCalls)
	}
	if gh.mergeCalls != 0 {
		t.Fatalf("already-merged epic must not merge again, got %d merges", gh.mergeCalls)
	}
	assertEpicCheckpointedMerged(t, p)
	if tr.setStatus != tracker.StageDone || !strings.Contains(tr.setExtra, "https://github.test/pr/42") {
		t.Fatalf("expected epic closed Done citing the merged PR, got %s %q", tr.setStatus, tr.setExtra)
	}
}

// "No commits between" with no merged PR behind it is a real failure — an empty
// epic branch has nothing to adopt, so the create error still surfaces.
func TestEnsureEpicPRNoCommitsWithoutMergedPRStillFails(t *testing.T) {
	gh := &epicGitHub{
		createErr: errors.New("gh pr create: exit status 1: GraphQL: No commits between main and epic/COD-1 (createPullRequest)"),
	}
	p := &Pipeline{Base: "main", EpicID: "COD-1", Git: baseCurrentGit{}, GitHub: gh, Tracker: &epicTracker{title: "x"}}
	if _, err := p.ensureEpicPR(context.Background(), "epic/COD-1-x", false); err == nil {
		t.Fatal("expected create error to surface when no merged PR exists")
	}
}

// With AUTO_MERGE=0 the epic release PR waits for the operator to merge it by hand;
// once they do, the epic closes with the shipped-to-base comment exactly as if
// auto-merge had merged it, and the wait announces itself once through the
// notification pathway attributed to the epic id.
func TestFinalizeEpicManualMergeWaitsThenShips(t *testing.T) {
	tr := doneEpicTracker()
	gh := &waitGitHub{
		epicGitHub: epicGitHub{
			createURL: "https://github.test/pr/42",
			checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
		},
		replies: []prReply{{state: "OPEN"}, {state: "OPEN"}, {state: "MERGED"}},
	}
	p := newEpicWaitPipeline(t, gh, tr)
	var buf bytes.Buffer
	p.Events = event.New(&buf)

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.createCalls != 1 {
		t.Fatalf("expected one epic PR create, got %d", gh.createCalls)
	}
	if gh.base != "main" || gh.head != "epic/COD-1-checkout-rebuild" {
		t.Fatalf("unexpected PR base/head: %s <- %s", gh.base, gh.head)
	}
	if gh.mergeCalls != 0 {
		t.Fatalf("AUTO_MERGE=0 must leave the merge to the human, got %d merges", gh.mergeCalls)
	}
	if tr.quarantineCalls != 0 {
		t.Fatalf("a merged epic must not be quarantined, got %d", tr.quarantineCalls)
	}
	if tr.setID != "COD-1" || tr.setStatus != tracker.StageDone {
		t.Fatalf("expected epic set Done, got %s %s", tr.setID, tr.setStatus)
	}
	if !strings.Contains(tr.setExtra, "merged to main") {
		t.Fatalf("expected the shipped-to-base comment, got %q", tr.setExtra)
	}
	assertEpicCheckpointedMerged(t, p)

	evs := awaitingMergeEvents(t, &buf)
	if len(evs) != 1 {
		t.Fatalf("emitted %d awaiting_merge events, want exactly 1", len(evs))
	}
	if got := strField(evs[0].Fields, "ticket"); got != "COD-1" {
		t.Errorf("ticket field = %q, want the epic id", got)
	}
	if got := strField(evs[0].Fields, "pr"); got != "42" {
		t.Errorf("pr field = %q, want 42", got)
	}
	if got := strField(evs[0].Fields, "url"); got != "https://github.test/pr/42" {
		t.Errorf("url field = %q, want the PR url", got)
	}
}

// An epic release PR closed without merging is a human rejection: give up
// (quarantine + needs-human) naming the epic PR, do NOT ship, and never close the
// Linear epic as done.
func TestFinalizeEpicManualMergeClosedNotShipped(t *testing.T) {
	tr := doneEpicTracker()
	gh := &waitGitHub{
		epicGitHub: epicGitHub{
			createURL: "https://github.test/pr/42",
			checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
		},
		replies: []prReply{{state: "OPEN"}, {state: "CLOSED"}},
	}
	p := newEpicWaitPipeline(t, gh, tr)

	err := p.FinalizeEpic(context.Background())
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("FinalizeEpic = %v, want a *GiveUpError", err)
	}
	if !strings.Contains(g.Reason, "epic PR #42 closed without merge") {
		t.Errorf("give-up reason = %q, want it to name the closed epic PR", g.Reason)
	}
	if tr.quarantineCalls != 1 || tr.quarantineID != "COD-1" {
		t.Errorf("Quarantine = %d call(s) on %q, want 1 on COD-1", tr.quarantineCalls, tr.quarantineID)
	}
	if tr.setStatus == tracker.StageDone {
		t.Errorf("a rejected epic must not be closed as done, got %q", tr.setStatus)
	}
	if got := p.State.Get("COD-1", "PHASE"); got != state.Quarantined {
		t.Errorf("epic PHASE = %q, want quarantined", got)
	}
	if got := p.State.Get("COD-1", "PR_STATUS"); got != "closed" {
		t.Errorf("epic PR_STATUS = %q, want closed", got)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("a rejected epic must not be merged, got %d", gh.mergeCalls)
	}
}

// A context canceled mid-wait is a blameless stop: FinalizeEpic propagates the
// cancellation without quarantining, and a later rerun — after the operator merged
// the PR while the loop was stopped — reconciles the merge and ships the epic.
func TestFinalizeEpicManualMergeCancelThenRerunReconciles(t *testing.T) {
	tr := doneEpicTracker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gh := &waitGitHub{
		epicGitHub: epicGitHub{
			createURL: "https://github.test/pr/42",
			checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
		},
		replies: []prReply{{state: "OPEN"}},
		onCall: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	p := newEpicWaitPipeline(t, gh, tr)

	err := p.FinalizeEpic(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FinalizeEpic = %v, want context.Canceled", err)
	}
	if tr.quarantineCalls != 0 {
		t.Fatalf("a stop is blameless — Quarantine called %d times, want 0", tr.quarantineCalls)
	}
	if tr.setStatus == tracker.StageDone {
		t.Fatalf("a stopped epic must not be closed, got %q", tr.setStatus)
	}
	if got := p.State.Get("COD-1", "PR_STATUS"); got != "" {
		t.Fatalf("epic PR_STATUS = %q, want none — nothing shipped", got)
	}

	p.GitHub = &waitGitHub{
		epicGitHub: epicGitHub{createURL: "https://github.test/pr/42"},
		replies:    []prReply{{state: "MERGED"}},
	}
	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("rerun FinalizeEpic returned error: %v", err)
	}
	if tr.setStatus != tracker.StageDone || !strings.Contains(tr.setExtra, "merged to main") {
		t.Fatalf("rerun must reconcile the merge and ship, got %s %q", tr.setStatus, tr.setExtra)
	}
	assertEpicCheckpointedMerged(t, p)
}

// assertEpicCheckpointedMerged pins the shipped epic to a complete run row rather
// than a bare PR_STATUS stamp: a checkpoint carrying only the status would have no
// phase, which the board reads as a run still in flight forever.
func assertEpicCheckpointedMerged(t *testing.T, p *Pipeline) {
	t.Helper()
	if got := p.State.Get("COD-1", "PHASE"); got != state.Merged {
		t.Fatalf("epic PHASE = %q, want merged", got)
	}
	if got := p.State.Get("COD-1", "PR_STATUS"); got != "merged" {
		t.Fatalf("epic PR_STATUS = %q, want merged", got)
	}
	if got := p.State.Get("COD-1", "TITLE"); got != "Checkout rebuild" {
		t.Fatalf("epic TITLE = %q, want the epic title", got)
	}
	if got := p.State.Get("COD-1", "PR_URL"); got != "https://github.test/pr/42" {
		t.Fatalf("epic PR_URL = %q, want the epic PR url", got)
	}
}

// assertEpicHandedOff pins a parked release: the phase stays releasing — the epic
// is still mid-release — beside the marker that says a human owns it.
func assertEpicHandedOff(t *testing.T, p *Pipeline) {
	t.Helper()
	if got := p.State.Get("COD-1", "PHASE"); got != state.Releasing {
		t.Fatalf("epic PHASE = %q, want %q", got, state.Releasing)
	}
	if got := p.State.Get("COD-1", "RELEASE"); got != state.ReleaseAwaitingHuman {
		t.Fatalf("epic RELEASE = %q, want %q", got, state.ReleaseAwaitingHuman)
	}
}

// releaseProbeGitHub reads the epic's checkpoint at CI-poll time, so a test can
// see the state a finalize runs under rather than only the state it lands in.
type releaseProbeGitHub struct {
	epicGitHub
	cps         state.Checkpoints
	seenPhase   string
	seenRelease string
	seenTitle   string
}

func (g *releaseProbeGitHub) Checks(ctx context.Context, pr string) ([]Check, error) {
	g.seenPhase = g.cps.Get("COD-1", "PHASE")
	g.seenRelease = g.cps.Get("COD-1", "RELEASE")
	g.seenTitle = g.cps.Get("COD-1", "TITLE")
	return g.epicGitHub.Checks(ctx, pr)
}

// conflictedEpicGit is a base-current fake whose merge with the base always
// conflicts and never comes clean, so the resolving agent runs out of attempts.
type conflictedEpicGit struct{ baseCurrentGit }

func (conflictedEpicGit) MergeRemote(context.Context, string, string) (bool, error) {
	return true, nil
}
func (conflictedEpicGit) Unmerged(context.Context) (string, error) {
	return "both modified: internal/x.go", nil
}

// The epic's own checkpoint brackets the shipping: releasing from the moment the
// last child is confirmed terminal, merged once the epic lands, with the title
// riding along so the row is never a phase-less one.
func TestFinalizeEpicBracketsShippingWithReleasing(t *testing.T) {
	tr := doneEpicTracker()
	gh := &releaseProbeGitHub{epicGitHub: epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}}
	p := shippableEpicPipeline(t, gh, tr)
	gh.cps = p.State

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.seenPhase != state.Releasing {
		t.Errorf("epic PHASE while shipping = %q, want %q", gh.seenPhase, state.Releasing)
	}
	if gh.seenRelease != state.ReleaseActive {
		t.Errorf("epic RELEASE while shipping = %q, want %q", gh.seenRelease, state.ReleaseActive)
	}
	if gh.seenTitle != "Checkout rebuild" {
		t.Errorf("epic TITLE while shipping = %q, want the epic title beside the phase", gh.seenTitle)
	}
	assertEpicCheckpointedMerged(t, p)
	if got := p.State.Get("COD-1", "RELEASE"); got != "" {
		t.Errorf("epic RELEASE after the merge = %q, want it cleared", got)
	}
}

// resumedEpicGit is the repo a killed finalize leaves behind: the epic branch is
// only discoverable by id (nothing cached it this run) and the tree still sits
// mid-merge where the resolving agent died.
type resumedEpicGit struct {
	baseCurrentGit
	branch      string
	midMerge    bool
	abortCalls  int
	checkedOut  []string
	mergedAfter bool
}

func (g *resumedEpicGit) FindEpicBranch(context.Context, string) (string, error) {
	return g.branch, nil
}
func (g *resumedEpicGit) MergeInProgress(context.Context) (bool, error) { return g.midMerge, nil }
func (g *resumedEpicGit) MergeAbort(context.Context) error {
	g.abortCalls++
	g.midMerge = false
	return nil
}
func (g *resumedEpicGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.checkedOut = append(g.checkedOut, ref)
	return nil
}
func (g *resumedEpicGit) MergeRemote(context.Context, string, string) (bool, error) {
	g.mergedAfter = !g.midMerge
	return false, nil
}

// adoptingEpicGitHub already has the epic PR an earlier finalize opened, so the
// resume adopts it instead of opening a second one.
type adoptingEpicGitHub struct {
	epicGitHub
	openURL string
}

func (g *adoptingEpicGitHub) PRURL(context.Context, string) (string, error) {
	return g.openURL, nil
}

// A finalize killed mid-release resumes from its own checkpoint: the epic branch is
// re-adopted by id, the half-merge the dead run left is aborted before the sync
// starts over, the PR that run opened is adopted rather than duplicated, and the
// epic ships. The release stops being resumable exactly when it lands.
func TestFinalizeEpicResumesFromReleasingCheckpoint(t *testing.T) {
	const epic = "epic/COD-1-checkout-rebuild"
	tr := doneEpicTracker()
	gh := &adoptingEpicGitHub{
		epicGitHub: epicGitHub{checks: []Check{{Name: "ci/test", Bucket: "pass"}}},
		openURL:    "https://github.test/pr/42",
	}
	git := &resumedEpicGit{branch: epic, midMerge: true}
	p := shippableEpicPipeline(t, gh, tr)
	p.Git = git
	p.exit = exitState{}
	if err := p.State.Set("COD-1", "PHASE", state.Releasing); err != nil {
		t.Fatal(err)
	}
	if err := p.State.Set("COD-1", "RELEASE", state.ReleaseActive); err != nil {
		t.Fatal(err)
	}
	if !p.ResumableRelease() {
		t.Fatal("a releasing checkpoint with no hand-off marker must be resumable")
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}

	if git.abortCalls != 1 {
		t.Errorf("MergeAbort calls = %d, want the half-merge aborted once on entry", git.abortCalls)
	}
	if !git.mergedAfter {
		t.Error("the sync merged while the tree was still mid-merge, want it restarted clean")
	}
	if !slices.Contains(git.checkedOut, epic) {
		t.Errorf("checkouts = %v, want the epic branch adopted by id", git.checkedOut)
	}
	if gh.createCalls != 0 {
		t.Errorf("CreatePR calls = %d, want the open epic PR adopted", gh.createCalls)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("merge calls = %d, want the resumed release to ship", gh.mergeCalls)
	}
	assertEpicCheckpointedMerged(t, p)
	if p.ResumableRelease() {
		t.Error("a shipped epic must stop reading as a release to resume")
	}
}

// A drift conflict the resolving agent could not clear leaves the epic PR to a
// human; the checkpoint says so instead of reading as trau still working on it,
// and the decline is typed so the caller never records a delivery over it.
func TestFinalizeEpicHandsOffWhenSyncConflictsRemain(t *testing.T) {
	tr := doneEpicTracker()
	gh := &epicGitHub{createURL: "https://github.test/pr/42"}
	p := shippableEpicPipeline(t, gh, tr)
	p.Git = conflictedEpicGit{}
	p.Runner = fakeRunner{}
	p.PhaseLogs = newMemPhaseLogs()
	p.RunsDir = t.TempDir()
	var buf bytes.Buffer
	p.Events = event.New(&buf)

	err := p.FinalizeEpic(context.Background())
	assertEpicHandOffError(t, err, "https://github.test/pr/42")
	if gh.mergeCalls != 0 {
		t.Fatalf("an unresolved conflict must not merge, got %d merges", gh.mergeCalls)
	}
	assertEpicHandedOff(t, p)
	assertEpicAwaitingMergeNotified(t, &buf)
}

// A gate that never went green is the same hand-off: the PR is left for review,
// the epic checkpoint records that a human owns the release now, and the operator
// hears about it.
func TestFinalizeEpicHandsOffWhenCINeverGreen(t *testing.T) {
	tr := doneEpicTracker()
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "fail"}},
	}
	p := shippableEpicPipeline(t, gh, tr)
	var buf bytes.Buffer
	p.Events = event.New(&buf)

	err := p.FinalizeEpic(context.Background())
	assertEpicHandOffError(t, err, "https://github.test/pr/42")
	assertEpicHandedOff(t, p)
	assertEpicAwaitingMergeNotified(t, &buf)
	if got := p.State.Get("COD-1", "PR_URL"); got != "https://github.test/pr/42" {
		t.Errorf("epic PR_URL = %q, want the handed-off PR recorded so a later merge can settle it", got)
	}
	if got := p.State.Get("COD-1", "PR_STATUS"); got != prStatusAwaitingMerge {
		t.Errorf("epic PR_STATUS = %q, want %q", got, prStatusAwaitingMerge)
	}
}

// A release that actually lands announces itself: an epic can drain in the
// background for hours, so the merge that ends it owes the operator a push
// carrying the PR rather than only the absence of a problem.
func TestFinalizeEpicNotifiesTheDelivery(t *testing.T) {
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, doneEpicTracker())
	var buf bytes.Buffer
	p.Events = event.New(&buf)

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	evs := deliveredEvents(t, &buf)
	if len(evs) != 1 {
		t.Fatalf("emitted %d epic_delivered events, want exactly 1", len(evs))
	}
	if got := strField(evs[0].Fields, "ticket"); got != "COD-1" {
		t.Errorf("ticket field = %q, want the epic id", got)
	}
	if got := strField(evs[0].Fields, "url"); got != "https://github.test/pr/42" {
		t.Errorf("url field = %q, want the epic PR url", got)
	}
	if !strings.Contains(evs[0].Msg, "https://github.test/pr/42") {
		t.Errorf("delivery message = %q, want it to name the PR", evs[0].Msg)
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("re-run FinalizeEpic returned error: %v", err)
	}
	if evs := deliveredEvents(t, &buf); len(evs) != 1 {
		t.Fatalf("emitted %d epic_delivered events after a re-finalize, want the news pushed once", len(evs))
	}
}

func deliveredEvents(t *testing.T, buf *bytes.Buffer) []event.Event {
	t.Helper()
	var out []event.Event
	for _, ev := range stateChangeEvents(t, buf) {
		if strField(ev.Fields, "state") == "epic_delivered" {
			out = append(out, ev)
		}
	}
	return out
}

// assertEpicHandOffError pins the typed decline a parked release ends on: the
// queue reads it to settle the item awaiting a human, and the reason it carries
// is what the item's card shows, so it must name the PR to land.
func assertEpicHandOffError(t *testing.T, err error, prURL string) {
	t.Helper()
	var h *EpicHandOffError
	if !errors.As(err, &h) {
		t.Fatalf("FinalizeEpic = %v, want an *EpicHandOffError", err)
	}
	if h.PRURL != prURL {
		t.Errorf("hand-off PRURL = %q, want %q", h.PRURL, prURL)
	}
	if prURL != "" && !strings.Contains(h.Error(), prURL) {
		t.Errorf("hand-off error = %q, want it to name the PR", h.Error())
	}
}

// assertEpicAwaitingMergeNotified pins the one notification a hand-off owes the
// operator: the same awaiting-merge pathway the ticket-level manual merge uses,
// attributed to the epic and carrying its PR.
func assertEpicAwaitingMergeNotified(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	evs := awaitingMergeEvents(t, buf)
	if len(evs) != 1 {
		t.Fatalf("emitted %d awaiting_merge events, want exactly 1", len(evs))
	}
	if got := strField(evs[0].Fields, "ticket"); got != "COD-1" {
		t.Errorf("ticket field = %q, want the epic id", got)
	}
	if got := strField(evs[0].Fields, "url"); got != "https://github.test/pr/42" {
		t.Errorf("url field = %q, want the epic PR url", got)
	}
}

// AUTO_MERGE=0 hands the green PR to the operator the moment the wait starts, so a
// wait that ends without a merge — here a stop mid-wait — leaves the release parked.
func TestFinalizeEpicHandsOffWhileAwaitingAManualMerge(t *testing.T) {
	tr := doneEpicTracker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gh := &waitGitHub{
		epicGitHub: epicGitHub{
			createURL: "https://github.test/pr/42",
			checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
		},
		replies: []prReply{{state: "OPEN"}},
		onCall: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	p := newEpicWaitPipeline(t, gh, tr)

	if err := p.FinalizeEpic(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("FinalizeEpic = %v, want context.Canceled", err)
	}
	assertEpicHandedOff(t, p)
}

// The local-delivery path gets the same bracket: an operator-owned merge parks the
// epic mid-release, and trau's own squash-merge lands it terminal.
func TestFinalizeEpicLocallyBracketsShippingWithReleasing(t *testing.T) {
	tests := []struct {
		name        string
		autoMerge   bool
		wantPhase   string
		wantRelease string
		wantHandOff bool
	}{
		{"operator merges it", false, state.Releasing, state.ReleaseAwaitingHuman, true},
		{"trau merges it", true, state.Merged, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := shippableEpicPipeline(t, &epicGitHub{}, doneEpicTracker())
			p.Git = &localGit{}
			p.AutoMerge = tc.autoMerge

			err := p.FinalizeEpic(context.Background())
			switch {
			case tc.wantHandOff:
				assertEpicHandOffError(t, err, "")
			case err != nil:
				t.Fatalf("FinalizeEpic returned error: %v", err)
			}
			if got := p.State.Get("COD-1", "PHASE"); got != tc.wantPhase {
				t.Errorf("epic PHASE = %q, want %q", got, tc.wantPhase)
			}
			if got := p.State.Get("COD-1", "RELEASE"); got != tc.wantRelease {
				t.Errorf("epic RELEASE = %q, want %q", got, tc.wantRelease)
			}
		})
	}
}

// Shipping is bracketed once: a second finalize of an epic that already merged —
// a re-queue, or a bare `trau --epic` re-run — leaves the terminal checkpoint
// alone rather than reopening the release it would then fail to finish.
func TestFinalizeEpicRerunLeavesAShippedEpicMerged(t *testing.T) {
	p := shippableEpicPipeline(t, &epicGitHub{}, doneEpicTracker())
	p.Git = &localGit{}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if got := p.State.Get("COD-1", "PHASE"); got != state.Merged {
		t.Fatalf("epic PHASE after the merge = %q, want %q", got, state.Merged)
	}

	p.Git = &localGit{checkoutErr: errors.New("pathspec 'main' did not match")}
	if err := p.FinalizeEpic(context.Background()); err == nil {
		t.Fatal("re-run FinalizeEpic = nil, want the checkout failure")
	}
	if got := p.State.Get("COD-1", "PHASE"); got != state.Merged {
		t.Errorf("epic PHASE after the re-run = %q, want it still %q", got, state.Merged)
	}
	if got := p.State.Get("COD-1", "RELEASE"); got != "" {
		t.Errorf("epic RELEASE after the re-run = %q, want it still cleared", got)
	}
}

func newEpicWaitPipeline(t *testing.T, gh GitHub, tr *epicTracker) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	return &Pipeline{
		Base:        "main",
		Remote:      "origin",
		EpicID:      "COD-1",
		exit:        exitState{epicBranch: "epic/COD-1-checkout-rebuild"},
		RequireCI:   config.CIGateOn,
		MergeMethod: "squash",
		Git:         baseCurrentGit{},
		GitHub:      gh,
		Tracker:     tr,
		State:       state.NewStore(dir),
		RunsDir:     dir,
		Sleep:       func(time.Duration) {},
	}
}

// shippableEpicPipeline wires an epic that ships end to end once its children are
// terminal: CI gated, auto-merge on, fake git and a fresh checkpoint store.
func shippableEpicPipeline(t *testing.T, gh GitHub, tr tracker.Tracker) *Pipeline {
	t.Helper()
	return &Pipeline{
		Base:        "main",
		Remote:      "origin",
		EpicID:      "COD-1",
		exit:        exitState{epicBranch: "epic/COD-1-checkout-rebuild"},
		AutoMerge:   true,
		RequireCI:   config.CIGateOn,
		MergeMethod: "squash",
		Git:         baseCurrentGit{},
		GitHub:      gh,
		Tracker:     tr,
		State:       state.NewStore(t.TempDir()),
	}
}

func doneEpicTracker() *epicTracker {
	return &epicTracker{
		title: "Checkout rebuild",
		subs: []tracker.SubIssue{
			{ID: "COD-2", Title: "first"},
			{ID: "COD-3", Title: "second"},
		},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
			"COD-3": tracker.StatusDone,
		},
	}
}

// A child the tracker does not report closed still blocks the epic — the
// checkpoint escape hatch only covers work trau itself merged AND closed, and an
// unreadable status is never mistaken for delivery. The decline is typed and names
// the blockers, so the caller parks the epic rather than reading it as a delivery.
func TestFinalizeEpicWaitsWhenAnyChildOpen(t *testing.T) {
	tests := []struct {
		name       string
		status     tracker.IssueStatus
		checkpoint map[string]string
		wantOpen   string
	}{
		{name: "no checkpoint", status: tracker.StatusOpen, wantOpen: "COD-3"},
		{
			name:       "in-flight checkpoint",
			status:     tracker.StatusOpen,
			checkpoint: map[string]string{"PHASE": state.Verified},
			wantOpen:   "COD-3",
		},
		{
			name:       "unreadable status on a delivered child",
			status:     tracker.StatusUnknown,
			checkpoint: map[string]string{"PHASE": state.Merged, "TRACKER_DONE": "1"},
			wantOpen:   "COD-3 (unknown)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &epicTracker{
				title: "Checkout rebuild",
				subs: []tracker.SubIssue{
					{ID: "COD-2", Title: "first"},
					{ID: "COD-3", Title: "second"},
				},
				status: map[string]tracker.IssueStatus{
					"COD-2": tracker.StatusDone,
					"COD-3": tt.status,
				},
			}
			gh := &epicGitHub{createURL: "https://github.test/pr/42"}
			p := &Pipeline{
				Base:    "main",
				EpicID:  "COD-1",
				exit:    exitState{epicBranch: "epic/COD-1-checkout-rebuild"},
				GitHub:  gh,
				Tracker: tr,
				State:   state.NewStore(t.TempDir()),
			}
			for k, v := range tt.checkpoint {
				if err := p.State.Set("COD-3", k, v); err != nil {
					t.Fatal(err)
				}
			}

			var unfinalized *EpicUnfinalizedError
			if err := p.FinalizeEpic(context.Background()); !errors.As(err, &unfinalized) {
				t.Fatalf("FinalizeEpic = %v, want an *EpicUnfinalizedError", err)
			}
			if unfinalized.EpicID != "COD-1" || strings.Join(unfinalized.Open, ", ") != tt.wantOpen {
				t.Errorf("decline = %+v, want epic COD-1 waiting on %q", unfinalized, tt.wantOpen)
			}
			if gh.createCalls != 0 {
				t.Fatalf("open child must block epic PR creation, got %d creates", gh.createCalls)
			}
			if tr.setID != "" {
				t.Fatalf("open child must block epic close, set %s %s", tr.setID, tr.setStatus)
			}
		})
	}
}

// An external automation flipping a delivered child back to a started state after
// trau closed it must not orphan the epic: the merged checkpoint settles
// terminality and the regressed tracker status is restored to Done.
func TestFinalizeEpicShipsWhenTrackerRegressedChildIsCheckpointMerged(t *testing.T) {
	tr := &epicTracker{
		title: "Checkout rebuild",
		subs: []tracker.SubIssue{
			{ID: "COD-2", Title: "first"},
			{ID: "COD-3", Title: "second"},
		},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
			"COD-3": tracker.StatusStarted,
		},
	}
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, tr)
	for k, v := range map[string]string{"PHASE": state.Merged, "PR": "424", "TRACKER_DONE": "1"} {
		if err := p.State.Set("COD-3", k, v); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("delivered children must ship the epic, got %d merges", gh.mergeCalls)
	}
	reassert := tr.setFor("COD-3")
	if reassert == nil || reassert.stage != tracker.StageDone {
		t.Fatalf("regressed child must be re-asserted Done, got %+v", reassert)
	}
	if !strings.Contains(reassert.extra, "PR #424") {
		t.Errorf("re-assert comment = %q, want the delivering PR named", reassert.extra)
	}
	if closed := tr.setFor("COD-1"); closed == nil || closed.stage != tracker.StageDone {
		t.Fatalf("epic must still close, got %+v", closed)
	}
}

// A workflow whose QA gate keeps delivered work open (DELIVERED_STATE=READY FOR
// QA) parks every merged child in a started state. That is the delivery, not a
// regression: the epic ships on its own merge record and nothing is written back
// over the QA column the team owns.
func TestFinalizeEpicLeavesChildrenParkedInANonTerminalDeliveredState(t *testing.T) {
	tr := doneEpicTracker()
	tr.status["COD-3"] = tracker.StatusStarted
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, tr)
	p.DeliveredState = "READY FOR QA"
	for k, v := range map[string]string{"PHASE": state.Merged, "TRACKER_DONE": "1"} {
		if err := p.State.Set("COD-3", k, v); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("a child parked in the delivered state must ship the epic, got %d merges", gh.mergeCalls)
	}
	if set := tr.setFor("COD-3"); set != nil {
		t.Errorf("a child parked in the delivered state must be left alone, got %+v", set)
	}
}

// Under a non-terminal delivered state only a child that fell all the way back to
// an unstarted status counts as regressed — and the restoring comment names the
// state the workflow actually delivers to, not "Done".
func TestFinalizeEpicRestoresChildBehindANonTerminalDeliveredState(t *testing.T) {
	tr := doneEpicTracker()
	tr.status["COD-3"] = tracker.StatusOpen
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, tr)
	p.DeliveredState = "READY FOR QA"
	for k, v := range map[string]string{"PHASE": state.Merged, "PR": "424", "TRACKER_DONE": "1"} {
		if err := p.State.Set("COD-3", k, v); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	restored := tr.setFor("COD-3")
	if restored == nil || restored.stage != tracker.StageDone {
		t.Fatalf("a child behind the delivered state must be restored, got %+v", restored)
	}
	if !strings.Contains(restored.extra, "READY FOR QA") {
		t.Errorf("restore comment = %q, want the delivered state named", restored.extra)
	}
}

// DELIVERED_STATE is spelled in the project's own vocabulary, so a column no
// stage claims is still a live one and restores a child only from an unstarted
// status — the same as the QA gates trau does recognise. Only a delivered state
// that reads as terminal makes every live status a regression.
func TestBehindDeliveredState(t *testing.T) {
	cases := []struct {
		delivered string
		started   bool
	}{
		{delivered: "", started: true},
		{delivered: "Released", started: true},
		{delivered: "UAT"},
		{delivered: "Ready for Release"},
	}
	for _, tc := range cases {
		t.Run(tc.delivered, func(t *testing.T) {
			p := &Pipeline{DeliveredState: tc.delivered}
			if !p.behindDeliveredState(tracker.StatusOpen) {
				t.Error("an unstarted status is behind every delivered state")
			}
			if got := p.behindDeliveredState(tracker.StatusStarted); got != tc.started {
				t.Errorf("behindDeliveredState(started) = %v, want %v", got, tc.started)
			}
		})
	}
}

// An epic PR the gate could not merge has shipped nothing to the base, so the epic
// ticket goes to review beside it instead of closing over an unmerged branch.
func TestFinalizeEpicLeavesEpicOpenWhenPRIsNotMerged(t *testing.T) {
	tr := doneEpicTracker()
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "fail"}},
	}
	p := shippableEpicPipeline(t, gh, tr)

	assertEpicHandOffError(t, p.FinalizeEpic(context.Background()), "https://github.test/pr/42")
	if gh.mergeCalls != 0 {
		t.Fatalf("a red epic gate must not merge, got %d merges", gh.mergeCalls)
	}
	closed := tr.setFor("COD-1")
	if closed == nil || closed.stage != tracker.StageInReview {
		t.Fatalf("an unmerged epic must be left in review, got %+v", closed)
	}
	if !strings.Contains(closed.extra, "ready for review") {
		t.Errorf("epic comment = %q, want the PR flagged for review", closed.extra)
	}
}

// A child whose delivery trau never confirmed on the tracker (no TRACKER_DONE) is
// still mid-flight however far its own checkpoint got: the epic keeps waiting on
// it and nothing is written back — trau only restores a status it set itself.
func TestFinalizeEpicSkipsReassertWithoutTrackerDoneMarker(t *testing.T) {
	tr := doneEpicTracker()
	tr.status["COD-3"] = tracker.StatusStarted
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, tr)
	if err := p.State.Set("COD-3", "PHASE", state.Merged); err != nil {
		t.Fatal(err)
	}

	var unfinalized *EpicUnfinalizedError
	if err := p.FinalizeEpic(context.Background()); !errors.As(err, &unfinalized) {
		t.Fatalf("FinalizeEpic = %v, want an *EpicUnfinalizedError", err)
	}
	if gh.createCalls != 0 {
		t.Fatalf("an unconfirmed child must block the epic, got %d creates", gh.createCalls)
	}
	if tr.setID != "" {
		t.Fatalf("an unconfirmed child must not be written back, set %s %s", tr.setID, tr.setStatus)
	}
}

// A failed re-assert is a best-effort miss: the merged checkpoint already proves
// delivery, so the epic ships anyway.
func TestFinalizeEpicShipsWhenReassertFails(t *testing.T) {
	inner := &epicTracker{
		title: "Checkout rebuild",
		subs:  []tracker.SubIssue{{ID: "COD-2", Title: "first"}},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusStarted,
		},
	}
	tr := &childSetFailTracker{epicTracker: inner, epicID: "COD-1"}
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, tr)
	for k, v := range map[string]string{"PHASE": state.Merged, "TRACKER_DONE": "1"} {
		if err := p.State.Set("COD-2", k, v); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("a failed re-assert must not block the epic, got %d merges", gh.mergeCalls)
	}
	if closed := inner.setFor("COD-1"); closed == nil || closed.stage != tracker.StageDone {
		t.Fatalf("epic must still close, got %+v", closed)
	}
}

func TestFinalizeEpicSurfacesUnknownChildStatusErrors(t *testing.T) {
	tr := &epicTracker{
		subs:      []tracker.SubIssue{{ID: "COD-2", Title: "first"}},
		statusErr: errors.New("tracker unavailable"),
	}
	p := &Pipeline{EpicID: "COD-1", Tracker: tr}

	err := p.FinalizeEpic(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status COD-2") {
		t.Fatalf("expected child status error, got %v", err)
	}
}

func TestEpicBranchNameAdoptsRemoteWhenLocalMissing(t *testing.T) {
	g := &epicGit{remoteExists: true}
	p := &Pipeline{
		Base:    "main",
		Remote:  "origin",
		EpicID:  "COD-1",
		Git:     g,
		Tracker: &epicTracker{title: "Checkout rebuild"},
	}

	branch, err := p.epicBranchName(context.Background())
	if err != nil {
		t.Fatalf("epicBranchName returned error: %v", err)
	}
	if branch != "epic/COD-1-checkout-rebuild" {
		t.Fatalf("unexpected branch: %s", branch)
	}
	if !g.adopted {
		t.Fatalf("expected the remote epic branch to be adopted")
	}
	if g.created {
		t.Fatalf("must not recreate the epic branch off base when the remote exists")
	}
}

func TestEpicBranchNameCreatesWhenNeitherExists(t *testing.T) {
	g := &epicGit{}
	p := &Pipeline{
		Base:    "main",
		Remote:  "origin",
		EpicID:  "COD-1",
		Git:     g,
		Tracker: &epicTracker{title: "Checkout rebuild"},
	}

	if _, err := p.epicBranchName(context.Background()); err != nil {
		t.Fatalf("epicBranchName returned error: %v", err)
	}
	if g.adopted {
		t.Fatalf("nothing to adopt when the remote branch is absent")
	}
	if !g.created || g.createBase != "origin/main" {
		t.Fatalf("expected fresh epic created off origin/main, created=%v base=%q", g.created, g.createBase)
	}
}

// A brand-new epic branch is cut from the base as the REMOTE has it, fetched first.
// An epic born from a local base that drifted behind starts life missing those
// commits, and every child PR then diffs against them as if the child had written
// them. Only a remote tip that cannot be resolved at all falls back to the local ref.
func TestEpicBranchNameCutsFromFetchedRemoteBase(t *testing.T) {
	tests := []struct {
		name      string
		git       *epicGit
		wantBase  string
		wantFetch string
	}{
		{
			name:      "remote reachable",
			git:       &epicGit{},
			wantBase:  "origin/main",
			wantFetch: "origin/main",
		},
		{
			name:      "fetch failed but the remote tip is known",
			git:       &epicGit{fetchErr: errors.New("could not read from remote")},
			wantBase:  "origin/main",
			wantFetch: "origin/main",
		},
		{
			name:      "no remote tip to cut from",
			git:       &epicGit{fetchErr: errors.New("could not read from remote"), unknownTip: true},
			wantBase:  "main",
			wantFetch: "origin/main",
		},
		{
			name:      "fetch succeeded but left no tracking ref",
			git:       &epicGit{unknownTip: true},
			wantBase:  "main",
			wantFetch: "origin/main",
		},
		{
			name:     "local delivery has no remote base",
			git:      &epicGit{noRemote: true},
			wantBase: "main",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pipeline{
				Base:    "main",
				Remote:  "origin",
				EpicID:  "COD-1",
				Git:     tt.git,
				Tracker: &epicTracker{title: "Checkout rebuild"},
			}

			if _, err := p.epicBranchName(context.Background()); err != nil {
				t.Fatalf("epicBranchName returned error: %v", err)
			}
			if !tt.git.created || tt.git.createBase != tt.wantBase {
				t.Errorf("epic cut from %q (created=%v), want %q", tt.git.createBase, tt.git.created, tt.wantBase)
			}
			if tt.git.fetched != tt.wantFetch {
				t.Errorf("fetched %q, want %q", tt.git.fetched, tt.wantFetch)
			}
		})
	}
}

// A renamed epic (whose title now slugs differently) must still resolve to its
// EXISTING branch — matched by epic ID, not the title slug — and never create a
// second one. This is the regression guard for the duplicate-epic-branch bug.
func TestEpicBranchNameAdoptsExistingDespiteTitleDrift(t *testing.T) {
	g := &epicGit{localExists: true, existing: "epic/COD-1-original-short-slug"}
	p := &Pipeline{
		Base:    "main",
		Remote:  "origin",
		EpicID:  "COD-1",
		Git:     g,
		Tracker: &epicTracker{title: "A Much Longer Renamed Title That Slugs Differently Now"},
	}

	branch, err := p.epicBranchName(context.Background())
	if err != nil {
		t.Fatalf("epicBranchName returned error: %v", err)
	}
	if branch != "epic/COD-1-original-short-slug" {
		t.Fatalf("expected the existing epic branch despite title drift, got %s", branch)
	}
	if g.created {
		t.Fatalf("must not create a second epic branch when one already exists")
	}
	if g.adopted {
		t.Fatalf("a local branch needs no remote adoption")
	}
}

func TestEpicBranchNameSurfacesRemoteCheckError(t *testing.T) {
	g := &epicGit{remoteErr: errors.New("remote unreachable")}
	p := &Pipeline{
		Base:    "main",
		Remote:  "origin",
		EpicID:  "COD-1",
		Git:     g,
		Tracker: &epicTracker{title: "Checkout rebuild"},
	}

	_, err := p.epicBranchName(context.Background())
	if err == nil || !strings.Contains(err.Error(), "check remote") {
		t.Fatalf("expected remote-check error, got %v", err)
	}
	if g.created || g.adopted {
		t.Fatalf("an indeterminate remote must neither recreate nor adopt (created=%v adopted=%v)", g.created, g.adopted)
	}
}

// epicGit is a fakeGit that drives epicBranchName's local/remote branch
// resolution and records whether it recreated or adopted the epic branch, off
// which base, and whether the remote base was fetched first.
type epicGit struct {
	fakeGit
	localExists  bool
	remoteExists bool
	remoteErr    error
	existing     string // branch name the finders report; defaults via existingOr
	noRemote     bool   // the repo has no remote at all (local delivery)
	fetchErr     error
	unknownTip   bool // the remote-tracking base tip does not resolve locally either
	fetched      string
	created      bool
	adopted      bool
	createBase   string
}

func (g *epicGit) FindEpicBranch(_ context.Context, id string) (string, error) {
	if g.localExists {
		return g.existingOr(id), nil
	}
	return "", nil
}
func (g *epicGit) FindRemoteEpicBranch(_ context.Context, _, id string) (string, error) {
	if g.remoteErr != nil {
		return "", g.remoteErr
	}
	if g.remoteExists {
		return g.existingOr(id), nil
	}
	return "", nil
}
func (g *epicGit) existingOr(id string) string {
	if g.existing != "" {
		return g.existing
	}
	return "epic/" + id + "-checkout-rebuild"
}
func (g *epicGit) RemoteExists(context.Context, string) (bool, error) { return !g.noRemote, nil }
func (g *epicGit) Fetch(_ context.Context, remote, branch string) error {
	g.fetched = remote + "/" + branch
	return g.fetchErr
}
func (g *epicGit) ResolvesToCommit(context.Context, string) (bool, error) {
	return !g.unknownTip, nil
}
func (g *epicGit) CheckoutRemoteBranch(context.Context, string, string) error {
	g.adopted = true
	return nil
}
func (g *epicGit) CreateBranch(_ context.Context, _, base string) error {
	g.created, g.createBase = true, base
	return nil
}

type epicTracker struct {
	title           string
	subs            []tracker.SubIssue
	status          map[string]tracker.IssueStatus
	statusErr       error
	setID           string
	setStatus       tracker.Stage
	setExtra        string
	sets            []trackerSet
	quarantineCalls int
	quarantineID    string
}

type trackerSet struct {
	id, extra string
	stage     tracker.Stage
}

// setFor returns the last status write aimed at id, or nil when there was none.
func (e *epicTracker) setFor(id string) *trackerSet {
	for i := len(e.sets) - 1; i >= 0; i-- {
		if e.sets[i].id == id {
			return &e.sets[i]
		}
	}
	return nil
}

// childSetFailTracker rejects every status write except the epic's own close, so a
// failed self-heal can be told apart from a failed epic close.
type childSetFailTracker struct {
	*epicTracker
	epicID string
}

func (t *childSetFailTracker) SetStatus(ctx context.Context, id string, stage tracker.Stage, extra string) error {
	if id != t.epicID {
		return errors.New("tracker unavailable")
	}
	return t.epicTracker.SetStatus(ctx, id, stage, extra)
}

func (e *epicTracker) Pick(context.Context, tracker.Scope) (string, error) { return "", nil }
func (e *epicTracker) SubIssues(context.Context, string) ([]tracker.SubIssue, error) {
	return e.subs, nil
}
func (e *epicTracker) Title(context.Context, string) (string, error) { return e.title, nil }
func (e *epicTracker) SetStatus(_ context.Context, id string, stage tracker.Stage, extra string) error {
	e.setID, e.setStatus, e.setExtra = id, stage, extra
	e.sets = append(e.sets, trackerSet{id: id, stage: stage, extra: extra})
	return nil
}
func (e *epicTracker) Reset(context.Context, string) error { return nil }
func (e *epicTracker) Quarantine(_ context.Context, id, _ string) error {
	e.quarantineCalls++
	e.quarantineID = id
	return nil
}
func (e *epicTracker) FileBug(context.Context, string, string) (string, error) {
	return "", nil
}
func (e *epicTracker) EnsureLabels(context.Context) error { return nil }
func (e *epicTracker) IssueStatus(_ context.Context, id string) (tracker.IssueStatus, error) {
	if e.statusErr != nil {
		return tracker.StatusUnknown, e.statusErr
	}
	return e.status[id], nil
}

// redThenGreenGitHub fails the epic's first CI poll and passes every one after, so
// the gate drives exactly one repair attempt before the merge.
type redThenGreenGitHub struct {
	epicGitHub
	polls int
}

func (g *redThenGreenGitHub) Checks(context.Context, string) ([]Check, error) {
	g.polls++
	if g.polls == 1 {
		return []Check{{Name: "ci/test", Bucket: "fail"}}, nil
	}
	return []Check{{Name: "ci/test", Bucket: "pass"}}, nil
}

// The CI gate, every repair attempt and the merge report under the epic's own id,
// so the stepper follows the epic to the base instead of going silent after the
// last child.
func TestEpicCIAndMergeReportsActivities(t *testing.T) {
	rec := &activityRecorder{}
	gh := &redThenGreenGitHub{}
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Thing"})
	p.GitHub = gh
	p.Remote = "origin"
	p.EpicID = "COD-7110"
	p.exit.epicBranch = "epic/COD-7110-thing"
	p.AutoMerge = true
	p.MergeMethod = "squash"
	p.MaxRepairs = 1
	p.OnActivity = rec.hook()

	merged, err := p.epicCIAndMerge(context.Background(), "https://github.test/pr/7")
	if err != nil {
		t.Fatalf("epicCIAndMerge err = %v", err)
	}
	if !merged {
		t.Fatal("expected the epic to merge once the repair turned CI green")
	}

	want := []reportedActivity{
		{"COD-7110", "ci-wait", ""},
		{"COD-7110", "merge", "epic-repair1/1"},
		{"COD-7110", "ci-wait", ""},
		{"COD-7110", "merge", ""},
	}
	if !reflect.DeepEqual(rec.seen, want) {
		t.Fatalf("activity reports = %+v, want %+v", rec.seen, want)
	}
}

type epicGitHub struct {
	createURL    string
	createErr    error
	mergedURL    string
	prState      string
	createCalls  int
	base         string
	head         string
	title        string
	body         string
	createDraft  bool
	readyCalls   int
	checks       []Check
	prCommits    int
	prFiles      int
	mergeCalls   int
	mergeMethod  string
	mergeDeleted bool
	closedPR     string
	closeErr     error
	bodyEdits    map[string]string
	editErr      error
}

func (e *epicGitHub) PRURL(context.Context, string) (string, error) { return "", nil }
func (e *epicGitHub) MergedPRURL(context.Context, string) (string, error) {
	return e.mergedURL, nil
}
func (e *epicGitHub) CreatePR(_ context.Context, base, head, title, body string, draft bool) (string, error) {
	e.createCalls++
	e.base, e.head, e.title, e.body, e.createDraft = base, head, title, body, draft
	return e.createURL, e.createErr
}
func (e *epicGitHub) MarkPRReady(context.Context, string) error { e.readyCalls++; return nil }
func (e *epicGitHub) UpdatePRBody(_ context.Context, pr, body string) error {
	if e.editErr != nil {
		return e.editErr
	}
	if e.bodyEdits == nil {
		e.bodyEdits = map[string]string{}
	}
	e.bodyEdits[pr] = body
	return nil
}

func (e *epicGitHub) PRState(context.Context, string) (string, error) { return e.prState, nil }
func (e *epicGitHub) Checks(context.Context, string) ([]Check, error) { return e.checks, nil }
func (e *epicGitHub) PRSize(context.Context, string) (int, int, error) {
	return e.prCommits, e.prFiles, nil
}
func (e *epicGitHub) ClosePR(_ context.Context, pr string) error {
	if e.closeErr != nil {
		return e.closeErr
	}
	e.closedPR, e.prState = pr, "CLOSED"
	return nil
}
func (e *epicGitHub) Merge(_ context.Context, _, method string, deleteBranch bool) error {
	e.mergeCalls++
	e.mergeMethod, e.mergeDeleted = method, deleteBranch
	return nil
}

func (e *epicGitHub) InStack(context.Context, string) (bool, error) { return false, nil }

// TestEpicPRTitle: the epic PR header is a conventional 'epic(<id>): <subject>' —
// case-conformed, stripped of stacked "Epic:" prefixes, and falling back to the id
// when the tracker title is empty.
func TestEpicPRTitle(t *testing.T) {
	cases := []struct {
		name, id, title, want string
	}{
		{"conventional header", "COD-951", "Atlas — architecture views per repo", "epic(COD-951): atlas — architecture views per repo"},
		{"stacked Epic prefixes stripped", "COD-951", "Epic: Epic: Atlas", "epic(COD-951): atlas"},
		{"empty title falls back to id", "COD-951", "", "epic(COD-951): COD-951"},
		{"acronym first word untouched", "COD-951", "API surface overhaul", "epic(COD-951): API surface overhaul"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := epicPRTitle(c.id, c.title); got != c.want {
				t.Errorf("epicPRTitle(%q, %q) = %q, want %q", c.id, c.title, got, c.want)
			}
		})
	}
}
