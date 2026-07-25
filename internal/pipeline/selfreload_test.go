package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// reloadGit records the post-ship rebuild's git traffic in order and can fail any
// step of it, so a test can assert both what ran and what a failure stopped.
type reloadGit struct {
	fakeGit
	ops         []string
	checkoutErr error
	pullErr     error
	discardErr  error
}

func (g *reloadGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.ops = append(g.ops, "checkout "+ref)
	return g.checkoutErr
}

func (g *reloadGit) Pull(_ context.Context, remote, branch string) error {
	g.ops = append(g.ops, "pull "+remote+" "+branch)
	return g.pullErr
}

func (g *reloadGit) DiscardTracked(context.Context) error {
	g.ops = append(g.ops, "discard")
	return g.discardErr
}

// reloadHub counts the reload requests the pipeline makes and can decline them
// the way the hub does for a repo that never opted in.
type reloadHub struct {
	calls int
	err   error
}

func (h *reloadHub) request(context.Context) (hubclient.ReloadAck, error) {
	h.calls++
	if h.err != nil {
		return hubclient.ReloadAck{}, h.err
	}
	return hubclient.ReloadAck{Pending: true, Version: "v2.5.0"}, nil
}

func newReloadPipeline(t *testing.T, git Git, gh GitHub, hub *reloadHub) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	return &Pipeline{
		Runner:            fakeRunner{},
		Tracker:           &fakeTracker{},
		Git:               git,
		GitHub:            gh,
		State:             state.NewStore(dir),
		RunsDir:           dir,
		RepoRoot:          t.TempDir(),
		Base:              "main",
		Remote:            "origin",
		Prefix:            "COD",
		AutoMerge:         true,
		MergeMethod:       "squash",
		Sleep:             func(time.Duration) {},
		HubSelfReload:     true,
		HubReloadBuildCmd: "true",
		RequestHubReload:  hub.request,
		probeBinary:       func(context.Context) (string, error) { return "v2.6.0", nil },
	}
}

// Only work that reached the base asks the hub to reload: a standalone merge and
// the reconcile of a PR a prior run already merged do, an epic slice (whose PR
// targets the epic branch) does not, and a repo without HUB_SELF_RELOAD never
// even checks the base out.
func TestCIAndMergeRequestsHubReloadOnlyForBaseShips(t *testing.T) {
	cases := []struct {
		name        string
		epicID      string
		selfReload  bool
		prState     string
		redCI       bool
		wantCalls   int
		wantRebuild bool
	}{
		{name: "standalone merge ships to base", selfReload: true, wantCalls: 1, wantRebuild: true},
		{name: "already-merged reconcile ships too", selfReload: true, prState: "MERGED", wantCalls: 1, wantRebuild: true},
		{name: "epic slice merges into the epic branch", selfReload: true, epicID: "COD-1", wantCalls: 0},
		{name: "red CI never shipped anything", selfReload: true, redCI: true, wantCalls: 0},
		{name: "key unset is a complete no-op", wantCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-1185"
			gh := &epicGitHub{prState: tc.prState}
			if tc.redCI {
				gh.checks = []Check{{Name: "ci/test", Bucket: "fail"}}
			}
			git := &reloadGit{}
			hub := &reloadHub{}
			p := newReloadPipeline(t, git, gh, hub)
			p.EpicID = tc.epicID
			p.HubSelfReload = tc.selfReload
			p.RequireCI = tc.redCI
			seedPROpen(t, p, id, "77", "feature/COD-1185-x")

			err := p.CIAndMerge(context.Background(), id)
			if tc.redCI {
				var g *GiveUpError
				if !errors.As(err, &g) {
					t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
				}
			} else if err != nil && !errors.Is(err, ErrAlreadyDone) {
				t.Fatalf("CIAndMerge = %v, want nil or ErrAlreadyDone", err)
			}
			if hub.calls != tc.wantCalls {
				t.Errorf("reload requests = %d, want %d", hub.calls, tc.wantCalls)
			}
			if rebuilt := slices.Contains(git.ops, "pull origin main"); rebuilt != tc.wantRebuild {
				t.Errorf("rebuilt from the base = %v, want %v (ops %v)", rebuilt, tc.wantRebuild, git.ops)
			}
			if !tc.selfReload && len(git.ops) != 0 {
				t.Errorf("HUB_SELF_RELOAD unset still touched git: %v", git.ops)
			}
		})
	}
}

// The AUTO_MERGE=0 path ships to the base just as the auto path does, so it asks
// for the same reload once the operator's merge lands.
func TestCIAndMergeManualMergeRequestsHubReload(t *testing.T) {
	id := "COD-1185M"
	gh := &waitGitHub{replies: []prReply{{state: "OPEN"}, {state: "MERGED"}}}
	hub := &reloadHub{}
	p := newReloadPipeline(t, &reloadGit{}, gh, hub)
	p.AutoMerge = false
	seedPROpen(t, p, id, "78", "feature/COD-1185M-x")

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if hub.calls != 1 {
		t.Fatalf("reload requests = %d, want 1", hub.calls)
	}
}

// The rebuild runs the repo's build command from the freshly pulled base and
// sweeps whatever tracked files it churned, so the next run still sees a clean base.
func TestReloadHubOntoBaseBuildsFromBaseAndSweeps(t *testing.T) {
	git := &reloadGit{}
	hub := &reloadHub{}
	p := newReloadPipeline(t, git, &epicGitHub{}, hub)
	p.HubReloadBuildCmd = "touch built"

	p.reloadHubOntoBase(context.Background())

	want := []string{"checkout main", "pull origin main", "discard"}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
	if _, err := os.Stat(filepath.Join(p.RepoRoot, "built")); err != nil {
		t.Errorf("build command did not run in the repo root: %v", err)
	}
	if hub.calls != 1 {
		t.Errorf("reload requests = %d, want 1", hub.calls)
	}
}

// Nothing in the post-ship step may fault the run: the work is already merged, so
// every failure logs and ends the step with the ticket still Done.
func TestReloadHubOntoBaseIsBlameless(t *testing.T) {
	cases := []struct {
		name       string
		git        *reloadGit
		buildCmd   string
		probeErr   error
		requestErr error
		wantCalls  int
		wantSwept  bool
	}{
		{name: "checkout fails", git: &reloadGit{checkoutErr: errors.New("worktree holds main")}},
		{name: "pull fails", git: &reloadGit{pullErr: errors.New("remote unreachable")}},
		{name: "build fails", git: &reloadGit{}, buildCmd: "exit 3", wantSwept: true},
		{name: "no build command", git: &reloadGit{}, buildCmd: " "},
		{name: "sweep fails", git: &reloadGit{discardErr: errors.New("index locked")}, wantSwept: true, wantCalls: 1},
		{name: "unusable binary", git: &reloadGit{}, probeErr: errors.New("exit status 2"), wantSwept: true},
		{name: "hub declines", git: &reloadGit{}, requestErr: errors.New("self-reload is off for loop"), wantSwept: true, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-1185B"
			hub := &reloadHub{err: tc.requestErr}
			p := newReloadPipeline(t, tc.git, &epicGitHub{}, hub)
			if tc.buildCmd != "" {
				p.HubReloadBuildCmd = tc.buildCmd
			}
			if tc.probeErr != nil {
				p.probeBinary = func(context.Context) (string, error) { return "", tc.probeErr }
			}
			seedPROpen(t, p, id, "79", "feature/COD-1185B-x")

			if err := p.CIAndMerge(context.Background(), id); err != nil {
				t.Fatalf("CIAndMerge = %v, want nil — a reload failure must never fault the run", err)
			}
			if got := p.State.Get(id, "PHASE"); got != state.Merged {
				t.Errorf("PHASE = %q, want merged", got)
			}
			if hub.calls != tc.wantCalls {
				t.Errorf("reload requests = %d, want %d", hub.calls, tc.wantCalls)
			}
			if swept := slices.Contains(tc.git.ops, "discard"); swept != tc.wantSwept {
				t.Errorf("build-artifact sweep = %v, want %v (ops %v)", swept, tc.wantSwept, tc.git.ops)
			}
		})
	}
}

// The hub only ever restarts onto a binary inside the repo that asked, so a loop
// started by hand from a PATH install — the test binary here — has nothing it can
// preflight and must not ask. Without the guard it would probe its own binary and
// submit a build it never looked at.
func TestReloadHubOntoBaseRefusesABinaryOutsideTheRepo(t *testing.T) {
	hub := &reloadHub{}
	log := &logRenderer{}
	p := newReloadPipeline(t, &reloadGit{}, &epicGitHub{}, hub)
	p.Renderer = log
	p.probeBinary = nil

	p.reloadHubOntoBase(context.Background())

	if hub.calls != 0 {
		t.Errorf("reload requests = %d, want 0", hub.calls)
	}
	if !log.contains("not a binary the hub could reload from") {
		t.Errorf("the binary was probed rather than refused, lines %v", log.lines)
	}
}

func TestWithinRepo(t *testing.T) {
	cases := []struct {
		root, path string
		want       bool
	}{
		{root: "/Users/rd/Projects/loop", path: "/Users/rd/Projects/loop/bin/trau", want: true},
		{root: "/Users/rd/Projects/loop/", path: "/Users/rd/Projects/loop/bin/../bin/trau", want: true},
		{root: "/Users/rd/Projects/loop", path: "/opt/homebrew/bin/trau"},
		{root: "/Users/rd/Projects/loop", path: "/Users/rd/Projects/loop-fork/bin/trau"},
		{root: "", path: "/opt/homebrew/bin/trau"},
	}
	for _, tc := range cases {
		if got := withinRepo(tc.root, tc.path); got != tc.want {
			t.Errorf("withinRepo(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
		}
	}
}

// An empty HUB_RELOAD_BUILD_CMD switches the rebuild off, so the step reports why
// and ends before it touches the working tree or the network.
func TestReloadHubOntoBaseSkipsWithoutABuildCommand(t *testing.T) {
	git := &reloadGit{}
	hub := &reloadHub{}
	log := &logRenderer{}
	p := newReloadPipeline(t, git, &epicGitHub{}, hub)
	p.Renderer = log
	p.HubReloadBuildCmd = " "

	p.reloadHubOntoBase(context.Background())

	if len(git.ops) != 0 {
		t.Errorf("git ops = %v, want none", git.ops)
	}
	if hub.calls != 0 {
		t.Errorf("reload requests = %d, want 0", hub.calls)
	}
	if !log.contains("no build command configured") {
		t.Errorf("the skip went unreported, lines %v", log.lines)
	}
}

// An operator stop cancels the run mid-step; the reload dies with it and says
// nothing, since a killed build is the stop working, not a failure to report.
func TestReloadHubOntoBaseStaysSilentOnStop(t *testing.T) {
	hub := &reloadHub{}
	log := &logRenderer{}
	p := newReloadPipeline(t, &reloadGit{}, &epicGitHub{}, hub)
	p.Renderer = log
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p.reloadHubOntoBase(ctx)

	if hub.calls != 0 {
		t.Errorf("reload requests = %d, want 0", hub.calls)
	}
	if log.contains("hub reload skipped") {
		t.Errorf("a stopped run warned about the reload, lines %v", log.lines)
	}
	if len(log.lines) != 0 {
		t.Errorf("a stopped run left a reload trace, lines %v", log.lines)
	}
}

// An epic that ships to the base asks for exactly one reload, and only once the
// epic is closed so a slow rebuild never holds the tracker open; an epic PR left
// open for review shipped nothing and asks for none.
func TestFinalizeEpicRequestsHubReloadOnlyWhenShipped(t *testing.T) {
	cases := []struct {
		name      string
		checks    []Check
		wantCalls int
	}{
		{name: "epic ships", checks: []Check{{Name: "ci/test", Bucket: "pass"}}, wantCalls: 1},
		{name: "epic PR left for review", checks: []Check{{Name: "ci/test", Bucket: "fail"}}, wantCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &epicTracker{
				title:  "Self reload",
				subs:   []tracker.SubIssue{{ID: "COD-2", Title: "first"}},
				status: map[string]tracker.IssueStatus{"COD-2": tracker.StatusDone},
			}
			gh := &epicGitHub{createURL: "https://github.test/pr/42", checks: tc.checks}
			hub := &reloadHub{}
			p := newReloadPipeline(t, &reloadGit{}, gh, hub)
			p.Tracker = tr
			p.EpicID = "COD-1"
			p.epicBranch = "epic/COD-1-self-reload"
			p.RequireCI = true
			closedFirst := false
			p.RequestHubReload = func(ctx context.Context) (hubclient.ReloadAck, error) {
				closedFirst = tr.setFor(p.EpicID) != nil
				return hub.request(ctx)
			}

			if err := p.FinalizeEpic(context.Background()); err != nil {
				t.Fatalf("FinalizeEpic = %v, want nil", err)
			}
			if hub.calls != tc.wantCalls {
				t.Errorf("reload requests = %d, want %d", hub.calls, tc.wantCalls)
			}
			if tc.wantCalls > 0 && !closedFirst {
				t.Error("the rebuild started before the epic was closed")
			}
		})
	}
}
