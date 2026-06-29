package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestFinalizeEpicCreatesPRAndClosesWhenChildrenTerminal(t *testing.T) {
	tr := &epicTracker{
		title: "Checkout rebuild",
		subs: []tracker.SubIssue{
			{ID: "COD-2", Title: "first"},
			{ID: "COD-3", Title: "second"},
		},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
			"COD-3": tracker.StatusCanceled,
		},
	}
	gh := &epicGitHub{createURL: "https://github.test/pr/42"}
	p := &Pipeline{
		Base:       "main",
		Remote:     "origin",
		EpicID:     "COD-1",
		epicBranch: "epic/COD-1-checkout-rebuild",
		Git:        fakeGit{},
		GitHub:     gh,
		Tracker:    tr,
	}

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
		t.Fatalf("AUTO_MERGE off must not merge the epic PR, got %d merges", gh.mergeCalls)
	}
	if tr.setID != "COD-1" || tr.setStatus != "Done" {
		t.Fatalf("expected epic set Done, got %s %s", tr.setID, tr.setStatus)
	}
	if !strings.Contains(tr.setExtra, "https://github.test/pr/42") {
		t.Fatalf("expected PR URL in close comment, got %q", tr.setExtra)
	}
}

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
		MergeMethod: "squash",
		Git:         fakeGit{},
		GitHub:      gh,
		Tracker:     tr,
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic returned error: %v", err)
	}
	if gh.mergeCalls != 1 {
		t.Fatalf("expected one epic merge on green CI, got %d", gh.mergeCalls)
	}
	if gh.mergeMethod != "squash" || !gh.mergeDeleted {
		t.Fatalf("expected squash merge with branch delete, got %q delete=%v", gh.mergeMethod, gh.mergeDeleted)
	}
	if tr.setStatus != "Done" || !strings.Contains(tr.setExtra, "merged to main") {
		t.Fatalf("expected epic closed as merged, got %s %q", tr.setStatus, tr.setExtra)
	}
}

func TestFinalizeEpicWaitsWhenAnyChildOpen(t *testing.T) {
	tr := &epicTracker{
		title: "Checkout rebuild",
		subs: []tracker.SubIssue{
			{ID: "COD-2", Title: "first"},
			{ID: "COD-3", Title: "second"},
		},
		status: map[string]tracker.IssueStatus{
			"COD-2": tracker.StatusDone,
			"COD-3": tracker.StatusOpen,
		},
	}
	gh := &epicGitHub{createURL: "https://github.test/pr/42"}
	p := &Pipeline{
		Base:       "main",
		EpicID:     "COD-1",
		epicBranch: "epic/COD-1-checkout-rebuild",
		GitHub:     gh,
		Tracker:    tr,
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
	title     string
	subs      []tracker.SubIssue
	status    map[string]tracker.IssueStatus
	statusErr error
	setID     string
	setStatus string
	setExtra  string
}

func (e *epicTracker) Pick(context.Context, tracker.Scope) (string, error) { return "", nil }
func (e *epicTracker) SubIssues(context.Context, string) ([]tracker.SubIssue, error) {
	return e.subs, nil
}
func (e *epicTracker) Title(context.Context, string) (string, error) { return e.title, nil }
func (e *epicTracker) SetStatus(_ context.Context, id, status, extra string) error {
	e.setID, e.setStatus, e.setExtra = id, status, extra
	return nil
}
func (e *epicTracker) Reset(context.Context, string) error              { return nil }
func (e *epicTracker) Quarantine(context.Context, string, string) error { return nil }
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
