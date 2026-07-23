package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
		epicBranch:  "epic/COD-1-checkout-rebuild",
		AutoMerge:   true,
		RequireCI:   true,
		MergeMethod: "squash",
		Git:         fakeGit{},
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
	if tr.setStatus != "Done" || !strings.Contains(tr.setExtra, "merged to main") {
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
		epicBranch:  "epic/COD-1-checkout-rebuild",
		AutoMerge:   true,
		RequireCI:   false,
		MergeMethod: "squash",
		Git:         fakeGit{},
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
	if tr.setStatus != "Done" {
		t.Fatalf("expected epic closed Done, got %s", tr.setStatus)
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
	if tr.setID != "COD-1" || tr.setStatus != "Done" {
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
	if tr.setStatus == "Done" {
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
	if tr.setStatus == "Done" {
		t.Fatalf("a stopped epic must not be closed, got %q", tr.setStatus)
	}
	if got := p.State.Get("COD-1", "PR_STATUS"); got != "" {
		t.Fatalf("epic PR_STATUS = %q, want none — an unshipped epic owns no run row", got)
	}

	p.GitHub = &waitGitHub{
		epicGitHub: epicGitHub{createURL: "https://github.test/pr/42"},
		replies:    []prReply{{state: "MERGED"}},
	}
	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("rerun FinalizeEpic returned error: %v", err)
	}
	if tr.setStatus != "Done" || !strings.Contains(tr.setExtra, "merged to main") {
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

func newEpicWaitPipeline(t *testing.T, gh GitHub, tr *epicTracker) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	return &Pipeline{
		Base:        "main",
		Remote:      "origin",
		EpicID:      "COD-1",
		epicBranch:  "epic/COD-1-checkout-rebuild",
		RequireCI:   true,
		MergeMethod: "squash",
		Git:         fakeGit{},
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
		epicBranch:  "epic/COD-1-checkout-rebuild",
		AutoMerge:   true,
		RequireCI:   true,
		MergeMethod: "squash",
		Git:         fakeGit{},
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
// unreadable status is never mistaken for delivery.
func TestFinalizeEpicWaitsWhenAnyChildOpen(t *testing.T) {
	tests := []struct {
		name       string
		status     tracker.IssueStatus
		checkpoint map[string]string
	}{
		{name: "no checkpoint", status: tracker.StatusOpen},
		{
			name:       "in-flight checkpoint",
			status:     tracker.StatusOpen,
			checkpoint: map[string]string{"PHASE": state.Verified},
		},
		{
			name:       "unreadable status on a delivered child",
			status:     tracker.StatusUnknown,
			checkpoint: map[string]string{"PHASE": state.Merged, "TRACKER_DONE": "1"},
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
				Base:       "main",
				EpicID:     "COD-1",
				epicBranch: "epic/COD-1-checkout-rebuild",
				GitHub:     gh,
				Tracker:    tr,
				State:      state.NewStore(t.TempDir()),
			}
			for k, v := range tt.checkpoint {
				if err := p.State.Set("COD-3", k, v); err != nil {
					t.Fatal(err)
				}
			}

			if err := p.FinalizeEpic(context.Background()); err != nil {
				t.Fatalf("FinalizeEpic returned error: %v", err)
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
	if reassert == nil || reassert.status != "Done" {
		t.Fatalf("regressed child must be re-asserted Done, got %+v", reassert)
	}
	if !strings.Contains(reassert.extra, "PR #424") {
		t.Errorf("re-assert comment = %q, want the delivering PR named", reassert.extra)
	}
	if closed := tr.setFor("COD-1"); closed == nil || closed.status != "Done" {
		t.Fatalf("epic must still close, got %+v", closed)
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

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
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
	if closed := inner.setFor("COD-1"); closed == nil || closed.status != "Done" {
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
	if !g.created || g.createBase != "main" {
		t.Fatalf("expected fresh epic created off main, created=%v base=%q", g.created, g.createBase)
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
// resolution and records whether it recreated or adopted the epic branch.
type epicGit struct {
	fakeGit
	localExists  bool
	remoteExists bool
	remoteErr    error
	existing     string // branch name the finders report; defaults via existingOr
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
	setStatus       string
	setExtra        string
	sets            []trackerSet
	quarantineCalls int
	quarantineID    string
}

type trackerSet struct{ id, status, extra string }

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

func (t *childSetFailTracker) SetStatus(ctx context.Context, id, status, extra string) error {
	if id != t.epicID {
		return errors.New("tracker unavailable")
	}
	return t.epicTracker.SetStatus(ctx, id, status, extra)
}

func (e *epicTracker) Pick(context.Context, tracker.Scope) (string, error) { return "", nil }
func (e *epicTracker) SubIssues(context.Context, string) ([]tracker.SubIssue, error) {
	return e.subs, nil
}
func (e *epicTracker) Title(context.Context, string) (string, error) { return e.title, nil }
func (e *epicTracker) SetStatus(_ context.Context, id, status, extra string) error {
	e.setID, e.setStatus, e.setExtra = id, status, extra
	e.sets = append(e.sets, trackerSet{id: id, status: status, extra: extra})
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

type epicGitHub struct {
	createURL    string
	createCalls  int
	base         string
	head         string
	title        string
	body         string
	checks       []Check
	mergeCalls   int
	mergeMethod  string
	mergeDeleted bool
}

func (e *epicGitHub) PRURL(context.Context, string) (string, error) { return "", nil }
func (e *epicGitHub) CreatePR(_ context.Context, base, head, title, body string) (string, error) {
	e.createCalls++
	e.base, e.head, e.title, e.body = base, head, title, body
	return e.createURL, nil
}
func (e *epicGitHub) PRState(context.Context, string) (string, error) { return "", nil }
func (e *epicGitHub) Checks(context.Context, string) ([]Check, error) { return e.checks, nil }
func (e *epicGitHub) Merge(_ context.Context, _, method string, deleteBranch bool) error {
	e.mergeCalls++
	e.mergeMethod, e.mergeDeleted = method, deleteBranch
	return nil
}

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
