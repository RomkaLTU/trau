package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// childGit is one Child repo's git in the fan-out test: it reports whether the
// build left work there and records what the ship phase did to it.
type childGit struct {
	fakeGit
	dirty   bool
	status  string
	branch  string
	commits []string
	pushed  []string
}

func (g *childGit) WorktreeDirty(context.Context) (bool, error) { return g.dirty, nil }

func (g *childGit) StatusPorcelain(context.Context) (string, error) { return g.status, nil }

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
	return nil
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
		"api-apigateway": {dirty: true},
		"api-companies":  {dirty: true},
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

	if err := p.CommitAndPR(context.Background(), id); err != nil {
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

// TestFolderResumeKeepsTheStartOfRunSweep is the resume contract: the off-limits
// census a run is judged against is the one taken before its build ran, so a run
// picked up from a checkpoint ships the children its own build dirtied instead of
// reading them back as off limits and giving up.
func TestFolderResumeKeepsTheStartOfRunSweep(t *testing.T) {
	id := "COD-93011"
	branch := "feature/COD-93011-resumed-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-a": {dirty: true},
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
