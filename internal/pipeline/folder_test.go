package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// childGit is one Child repo's git in the fan-out tests: it reports what the
// child's working tree carries and which branch it sits on (empty head meaning
// the base branch), and records what the ship phase did to it.
type childGit struct {
	fakeGit
	status        string
	head          string
	branch        string
	commits       []string
	pushed        []string
	deleted       []string
	deletedRemote []string
	checkedOut    []string
	cleaned       int
	hasBranch     bool // whether the ticket's branch already exists here
	baseBehind    bool // this child's remote base is missing the local base tip
	pushFixes     bool // whether pushing the base brings the remote current
}

func (g *childGit) WorktreeStatus(context.Context) (string, error) { return g.status, nil }

func (g *childGit) CurrentBranch(ctx context.Context) (string, error) {
	if g.head != "" {
		return g.head, nil
	}
	return g.fakeGit.CurrentBranch(ctx)
}

func (g *childGit) CreateBranch(_ context.Context, branch, _ string) error {
	g.branch = branch
	return nil
}

func (g *childGit) Commit(_ context.Context, message string, _ bool) error {
	g.commits = append(g.commits, message)
	return nil
}

func (g *childGit) Push(_ context.Context, _, ref string, _ bool) error {
	g.pushed = append(g.pushed, ref)
	if ref == prBaseBranch && g.pushFixes {
		g.baseBehind = false
	}
	return nil
}

func (g *childGit) RemoteSHA(context.Context, string, string) (string, error) {
	return prBaseRemoteTip, nil
}

func (g *childGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.checkedOut = append(g.checkedOut, ref)
	return nil
}

func (g *childGit) Clean(context.Context) error { g.cleaned++; return nil }

func (g *childGit) BranchExists(context.Context, string) (bool, error) {
	return g.hasBranch, nil
}

func (g *childGit) RemoteBranchExists(_ context.Context, _, _ string) (bool, error) {
	return g.hasBranch, nil
}

func (g *childGit) DeleteBranch(_ context.Context, branch string) error {
	g.deleted = append(g.deleted, branch)
	return nil
}

func (g *childGit) DeletePushedBranch(_ context.Context, _, branch string) error {
	g.deletedRemote = append(g.deletedRemote, branch)
	return nil
}

func (g *childGit) IsAncestor(context.Context, string, string) (bool, error) {
	return !g.baseBehind, nil
}

// TestFolderShipFansOutToEveryChangedChild is the Folder repo delivery contract:
// the ticket's branch is cut — with the same name — in each Child repo the build
// changed and in no other, each gets its own PR, and the checkpoint records the
// whole ship set.
func TestFolderShipFansOutToEveryChangedChild(t *testing.T) {
	id := "COD-93010"
	branch := "feature/COD-93010-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-apigateway": {},
		"api-companies":  {},
		"api-billing":    {},
	}
	ghs := map[string]*epicGitHub{
		"api-apigateway": {createURL: "https://github.com/acme/api-apigateway/pull/7"},
		"api-companies":  {createURL: "https://github.com/acme/api-companies/pull/3"},
		"api-billing":    {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-apigateway"].status = " M gateway.go"
	gits["api-companies"].status = "?? companies/client.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}

	for _, name := range []string{"api-apigateway", "api-companies"} {
		g := gits[name]
		if g.branch != branch {
			t.Errorf("%s branch = %q, want %q", name, g.branch, branch)
		}
		if len(g.commits) != 1 {
			t.Errorf("%s commits = %v, want exactly one", name, g.commits)
		}
		if len(g.pushed) != 1 || g.pushed[0] != branch {
			t.Errorf("%s pushed = %v, want [%s]", name, g.pushed, branch)
		}
		if ghs[name].createCalls != 1 {
			t.Errorf("%s opened %d PRs, want 1", name, ghs[name].createCalls)
		}
	}

	untouched := gits["api-billing"]
	if untouched.branch != "" || len(untouched.commits) > 0 || ghs["api-billing"].createCalls > 0 {
		t.Errorf("api-billing was changed by nothing but got branch %q, commits %v, %d PRs",
			untouched.branch, untouched.commits, ghs["api-billing"].createCalls)
	}

	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-apigateway,api-companies" {
		t.Errorf("SHIP_TARGETS = %q, want the two changed children", got)
	}
	urls := p.State.Get(id, "PR_URLS")
	for _, want := range []string{"api-apigateway=" + ghs["api-apigateway"].createURL, "api-companies=" + ghs["api-companies"].createURL} {
		if !strings.Contains(urls, want) {
			t.Errorf("PR_URLS = %q, want it to carry %q", urls, want)
		}
	}
	if got := p.State.Get(id, "PR_URL"); got != ghs["api-apigateway"].createURL {
		t.Errorf("PR_URL = %q, want the first target's PR", got)
	}
}

// TestFolderShipLeavesChildrenTheRunFoundDirty is the off-limits contract at ship
// time: a child an operator left uncommitted work in — an untracked file is work —
// is named off limits and then left exactly as it was found. Only the build's own
// change makes a child a ship target, so the untouched child gets no branch, no
// commit and no PR, and its dirt does not give the run up either.
func TestFolderShipLeavesChildrenTheRunFoundDirty(t *testing.T) {
	id := "COD-93012"
	branch := "feature/COD-93012-one-child-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-a": {},
		"api-b": {},
		"api-c": {status: "?? scratch.md", head: "wip/local"},
	}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/4"},
		"api-b": {},
		"api-c": {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-a"].status = " M handler.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}

	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a" {
		t.Errorf("SHIP_TARGETS = %q, want only the child the build changed", got)
	}
	if got := p.State.Get(id, offLimitsKey); !strings.Contains(got, "api-c=it has uncommitted changes") {
		t.Errorf("%s = %q, want api-c named for the work it already carried", offLimitsKey, got)
	}
	stray := gits["api-c"]
	if stray.branch != "" || len(stray.commits) > 0 || len(stray.pushed) > 0 || ghs["api-c"].createCalls > 0 {
		t.Errorf("api-c was dirty before the run and untouched by it, but got branch %q, commits %v, pushes %v, %d PRs",
			stray.branch, stray.commits, stray.pushed, ghs["api-c"].createCalls)
	}
}

// TestFolderResumeKeepsTheStartOfRunSweep is the resume contract: the off-limits
// census a run is judged against is the one taken before its build ran, so a run
// picked up from a checkpoint ships the children its own build dirtied instead of
// reading them back as off limits and giving up.
func TestFolderResumeKeepsTheStartOfRunSweep(t *testing.T) {
	id := "COD-93011"
	branch := "feature/COD-93011-resumed-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-a": {},
		"api-b": {},
	}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/9"},
		"api-b": {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)

	// What the interrupted build left behind is exactly what a second sweep would
	// read as an unclean child.
	gits["api-a"].status = " M service.go"
	p.resetFolderRun()
	p.startFolderRun(ctx, id, true)

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR after resume: %v", err)
	}
	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a" {
		t.Errorf("SHIP_TARGETS = %q, want the child the build changed", got)
	}
	if ghs["api-a"].createCalls != 1 {
		t.Errorf("api-a opened %d PRs, want 1", ghs["api-a"].createCalls)
	}
}

// TestFolderResetDropsShipTargetsAndSparesUnrelatedWIP is the Folder repo undo
// contract: every recorded ship target loses its branch on both sides and its
// working tree, and a child the run never shipped to is left exactly as it was
// found — whether its work was already there when the run started or an operator
// added it while the run was in flight.
func TestFolderResetDropsShipTargetsAndSparesUnrelatedWIP(t *testing.T) {
	id := "COD-93014"
	branch := "feature/COD-93014-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-companies": {hasBranch: true},
		"api-billing":   {status: " M billing.go"},
		"api-users":     {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-companies"].status = " M companies.go"
	gits["api-users"].status = "?? midrun.txt"
	for key, value := range map[string]string{"BRANCH": branch, "PHASE": "building", "SHIP_TARGETS": "api-companies"} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	p.resetLocal(ctx, id)

	shipped := gits["api-companies"]
	if !slices.Contains(shipped.deleted, branch) || !slices.Contains(shipped.deletedRemote, branch) {
		t.Errorf("api-companies deleted %v locally and %v on the remote, want %s in both", shipped.deleted, shipped.deletedRemote, branch)
	}
	if !slices.Contains(shipped.checkedOut, p.Base) || shipped.cleaned == 0 {
		t.Errorf("api-companies checked out %v and cleaned %d times, want it back on %s with its loose work gone", shipped.checkedOut, shipped.cleaned, p.Base)
	}
	wip := gits["api-billing"]
	if len(wip.deleted) > 0 || len(wip.checkedOut) > 0 || wip.cleaned > 0 {
		t.Errorf("api-billing carried WIP the run never touched but was reset: deleted %v, checked out %v, cleaned %d times", wip.deleted, wip.checkedOut, wip.cleaned)
	}
	if midrun := gits["api-users"]; len(midrun.checkedOut) > 0 || midrun.cleaned > 0 {
		t.Errorf("api-users was dirtied after the run shipped elsewhere but got checkouts %v and %d cleans", midrun.checkedOut, midrun.cleaned)
	}
}

// TestFolderRequeueClosesEveryPRTheRunOpened is the Folder repo requeue contract:
// a run that fanned out over several children closes all of its pull requests and
// drops all of its branches, not just the first target's.
func TestFolderRequeueClosesEveryPRTheRunOpened(t *testing.T) {
	id := "COD-93013"
	branch := "feature/COD-93013-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-apigateway": {hasBranch: true},
		"api-companies":  {hasBranch: true},
	}
	ghs := map[string]*epicGitHub{
		"api-apigateway": {prState: "OPEN"},
		"api-companies":  {prState: "OPEN"},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"BRANCH":       branch,
		"PHASE":        "pr_open",
		"SHIP_TARGETS": "api-apigateway,api-companies",
		"PR_URLS":      "api-apigateway=https://github.com/acme/api-apigateway/pull/7,api-companies=https://github.com/acme/api-companies/pull/3",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.Requeue(context.Background(), id, false); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	for name, pr := range map[string]string{"api-apigateway": "7", "api-companies": "3"} {
		if ghs[name].closedPR != pr {
			t.Errorf("%s closed PR %q, want %q", name, ghs[name].closedPR, pr)
		}
		if !slices.Contains(gits[name].deleted, branch) {
			t.Errorf("%s deleted branches %v, want %s among them", name, gits[name].deleted, branch)
		}
		if !slices.Contains(gits[name].deletedRemote, branch) {
			t.Errorf("%s deleted remote branches %v, want %s among them", name, gits[name].deletedRemote, branch)
		}
	}
}
