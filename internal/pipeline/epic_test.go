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
