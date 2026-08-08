package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestWorkRootRedirectsTheTreeButNotTheIdentity pins the seam: everything that reads or writes the working tree follows WorkTree, and
// everything the hub keys a record by keeps reading RepoRoot.
func TestWorkRootRedirectsTheTreeButNotTheIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shipflock")
	work := filepath.Join(t.TempDir(), "wt-COD-1")
	for _, dir := range []string{root, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		worktree string
		wantWork string
	}{
		{name: "no worktree", worktree: "", wantWork: root},
		{name: "worktree", worktree: work, wantWork: work},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pipeline{RepoRoot: root, WorkTree: tt.worktree, RunsDir: ".trau/runs"}
			if got := p.workRoot(); got != tt.wantWork {
				t.Errorf("workRoot() = %q, want %q", got, tt.wantWork)
			}
			if got, want := p.runDir("COD-1"), filepath.Join(tt.wantWork, ".trau/runs", "COD-1"); got != want {
				t.Errorf("runDir() = %q, want %q", got, want)
			}
			if got := p.repoLabel(); got != "shipflock" {
				t.Errorf("repoLabel() = %q, want shipflock — repo identity must not follow the worktree", got)
			}
			if got := p.RepoRoot; got != root {
				t.Errorf("RepoRoot = %q, want %q", got, root)
			}
		})
	}
}

// TestRunDirKeepsAnAbsoluteRunsDirAbsolute guards the one RUNS_DIR shape a
// worktree must not move: an absolute path is already fully resolved.
func TestRunDirKeepsAnAbsoluteRunsDirAbsolute(t *testing.T) {
	abs := t.TempDir()
	p := &Pipeline{RepoRoot: t.TempDir(), WorkTree: t.TempDir(), RunsDir: abs}
	if got, want := p.runDir("COD-2"), filepath.Join(abs, "COD-2"); got != want {
		t.Fatalf("runDir() = %q, want %q", got, want)
	}
}

// TestRepoCommandsRunInTheWorktree is the visible half of the redirect: the
// repo's own shell commands (lint-fix and every other runRepoCmd caller) execute
// in the given tree, so they can never edit the user's own checkout.
func TestRepoCommandsRunInTheWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd is not the shell's working-directory command on Windows")
	}
	root := t.TempDir()
	work := t.TempDir()
	p := &Pipeline{RepoRoot: root, WorkTree: work}

	out, err := p.runRepoCmd(context.Background(), "pwd", "pwd")
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("repo command ran in %q, want the worktree %q", got, want)
	}
}

// TestEnsureRepoConfigIncludeFromALinkedWorktree covers the wiring the ticket
// calls out: a linked worktree shares .git/config with the main checkout, so the
// include is written once and its relative value still resolves to the
// registered root's .gitconfig.repo. The main checkout then finds it already
// present rather than adding a second copy.
func TestEnsureRepoConfigIncludeFromALinkedWorktree(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-m", "seed")

	pinned := "[user]\n\tname = Pinned\n\temail = pinned@example.com\n"
	if err := os.WriteFile(filepath.Join(root, RepoConfigFile), []byte(pinned), 0o644); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(t.TempDir(), "wt-test")
	gitRun(t, root, "worktree", "add", "--detach", work, "main")

	added, err := EnsureRepoConfigInclude(context.Background(), root, work)
	if err != nil {
		t.Fatalf("wire from the worktree: %v", err)
	}
	if !added {
		t.Fatal("wiring from the worktree added no include")
	}

	out, err := exec.Command("git", "-C", work, "config", "user.email").Output()
	if err != nil {
		t.Fatalf("resolve user.email in the worktree: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "pinned@example.com" {
		t.Fatalf("worktree user.email = %q, want pinned@example.com", got)
	}

	added, err = EnsureRepoConfigInclude(context.Background(), root, "")
	if err != nil {
		t.Fatalf("re-wire from the main checkout: %v", err)
	}
	if added {
		t.Fatal("the main checkout added a second include — the shared config already carried it")
	}
	out, err = exec.Command("git", "-C", root, "config", "user.email").Output()
	if err != nil {
		t.Fatalf("resolve user.email in the main checkout: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "pinned@example.com" {
		t.Fatalf("main checkout user.email = %q, want pinned@example.com", got)
	}
}

// TestEnsureRepoConfigIncludeWithWorktreeConfigExtension checks the
// extensions.worktreeConfig case: an include the repository already pinned in
// the per-worktree config is found, so the shared config is left alone.
func TestEnsureRepoConfigIncludeWithWorktreeConfigExtension(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-m", "seed")
	pinned := "[user]\n\tname = Pinned\n\temail = pinned@example.com\n"
	if err := os.WriteFile(filepath.Join(root, RepoConfigFile), []byte(pinned), 0o644); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(t.TempDir(), "wt-ext")
	gitRun(t, root, "worktree", "add", "--detach", work, "main")
	gitRun(t, root, "config", "--local", "extensions.worktreeConfig", "true")
	gitRun(t, work, "config", "--worktree", "--add", "include.path", "../"+RepoConfigFile)

	added, err := EnsureRepoConfigInclude(context.Background(), root, work)
	if err != nil {
		t.Fatalf("wire from the worktree: %v", err)
	}
	if added {
		t.Fatal("added a shared include over one the per-worktree config already carried")
	}
}

// worktreeCleanBaseGit records the calls EnsureCleanBase would make against the
// user's own checkout, and reports a dirty tree so the stash path is armed.
type worktreeCleanBaseGit struct {
	fakeGit
	calls []string
}

func (g *worktreeCleanBaseGit) StatusPorcelain(context.Context) (string, error) {
	g.calls = append(g.calls, "status")
	return " M src/app.ts\n", nil
}
func (g *worktreeCleanBaseGit) Stash(context.Context, string) error {
	g.calls = append(g.calls, "stash")
	return nil
}
func (g *worktreeCleanBaseGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.calls = append(g.calls, "checkout "+ref)
	return nil
}
func (g *worktreeCleanBaseGit) CheckoutDetached(_ context.Context, ref string, _ bool) error {
	g.calls = append(g.calls, "checkout-detached "+ref)
	return nil
}
func (g *worktreeCleanBaseGit) Pull(context.Context, string, string) error {
	g.calls = append(g.calls, "pull")
	return nil
}
func (g *worktreeCleanBaseGit) Fetch(context.Context, string, string) error {
	g.calls = append(g.calls, "fetch")
	return nil
}

// TestWorktreesOffForFolderReposAndByDefault pins the two ways the feature stays
// out of the way: it is opt-in, and a folder of repositories — which has no
// repository at its root for git to add a tree to — ignores the opt-in.
func TestWorktreesOffForFolderReposAndByDefault(t *testing.T) {
	tests := []struct {
		name   string
		on     bool
		folder bool
		want   bool
	}{
		{name: "default", on: false, folder: false, want: false},
		{name: "on", on: true, folder: false, want: true},
		{name: "on for a folder repo", on: true, folder: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pipeline{RepoRoot: filepath.Join(t.TempDir(), "shipflock"), Worktrees: tt.on, FolderRepo: tt.folder}
			if got := p.worktreesOn(); got != tt.want {
				t.Errorf("worktreesOn() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWorktreePathIsComputedFromConfigAlone is what lets the hub, the TUI and the
// CLI all name the same tree without passing it to one another.
func TestWorktreePathIsComputedFromConfigAlone(t *testing.T) {
	p := &Pipeline{RepoRoot: filepath.Join("/src", "shipflock"), Worktrees: true, WorktreesDir: "/w"}
	if got, want := p.worktreePath("COD-1581"), filepath.Join("/w", "shipflock", "COD-1581"); got != want {
		t.Errorf("worktreePath = %q, want %q", got, want)
	}
}

// TestEnsureCleanBaseLeavesTheUsersCheckoutAloneWithWorktreesOn is the headline
// acceptance criterion: with worktrees on, a dirty checkout neither blocks the run
// nor gets stashed, and nothing is checked out in it.
func TestEnsureCleanBaseLeavesTheUsersCheckoutAloneWithWorktreesOn(t *testing.T) {
	g := &worktreeCleanBaseGit{}
	p := &Pipeline{
		RepoRoot: filepath.Join(t.TempDir(), "shipflock"), Git: g,
		Base: "main", Remote: "origin", Worktrees: true, WorktreesDir: t.TempDir(),
	}

	if err := p.EnsureCleanBase(context.Background()); err != nil {
		t.Fatalf("EnsureCleanBase: %v", err)
	}
	for _, call := range g.calls {
		if call != "fetch" {
			t.Errorf("EnsureCleanBase called %q on the user's checkout; only the base fetch is allowed", call)
		}
	}

	// The contrast, and the behaviour WORKTREES=0 must keep: the same dirty tree
	// aborts the run rather than being touched.
	off := &worktreeCleanBaseGit{}
	p2 := &Pipeline{RepoRoot: p.RepoRoot, Git: off, Base: "main", Remote: "origin"}
	if err := p2.EnsureCleanBase(context.Background()); err == nil {
		t.Error("WORKTREES=0 no longer blocks on a dirty checkout — the shared-checkout flow must be untouched")
	}
}

// worktreeRepo builds a one-commit repository and a Pipeline wired to work in it,
// with worktrees on and a fresh worktrees root.
func worktreeRepo(t *testing.T) (*Pipeline, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "shipflock")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-m", "seed")

	dir := filepath.Join(t.TempDir(), "worktrees")
	p := &Pipeline{
		RepoRoot:     root,
		State:        state.NewStore(filepath.Join(t.TempDir(), "runs")),
		Git:          ExecGit{Repo: root},
		GitAt:        func(r string) Git { return ExecGit{Repo: r} },
		Base:         "main",
		RunsDir:      ".trau/runs",
		Worktrees:    true,
		WorktreesDir: dir,
	}
	return p, root, dir
}

// TestPrepareWorktreeMovesTheRunIntoTheTreeAndReportsIt is the provisioning seam
// turned feature: the run's git and every tracked-file lookup follow the tree,
// RepoRoot — the key every hub record is written under — does not, and the hub is
// told which tree the ticket got.
func TestPrepareWorktreeMovesTheRunIntoTheTreeAndReportsIt(t *testing.T) {
	p, root, dir := worktreeRepo(t)
	var reported [3]string
	p.ReportWorktree = func(_ context.Context, ticket, path, branch string) error {
		reported = [3]string{ticket, path, branch}
		return nil
	}

	if err := p.prepareWorktree(context.Background(), "COD-1581"); err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}

	want := filepath.Join(dir, "shipflock", "COD-1581")
	if p.WorkTree != want {
		t.Fatalf("WorkTree = %q, want %q", p.WorkTree, want)
	}
	if p.workRoot() != want {
		t.Errorf("workRoot() = %q, want the tree %q", p.workRoot(), want)
	}
	if p.RepoRoot != root {
		t.Errorf("RepoRoot = %q, want the registered root %q — identity must not follow the tree", p.RepoRoot, root)
	}
	if got := p.Git.(ExecGit).Repo; got != want {
		t.Errorf("Git targets %q, want the tree %q", got, want)
	}
	if reported != [3]string{"COD-1581", want, ""} {
		t.Errorf("reported %v, want the ticket and its detached tree", reported)
	}
	if _, err := os.Stat(filepath.Join(want, "README.md")); err != nil {
		t.Errorf("the tree has no checkout: %v", err)
	}
}

// TestPrepareWorktreeAdoptsTheTicketsBranch covers resume: a branch already cut is
// checked out in the tree, so the run picks its own work back up.
func TestPrepareWorktreeAdoptsTheTicketsBranch(t *testing.T) {
	p, root, _ := worktreeRepo(t)
	gitRun(t, root, "branch", "feature/COD-1581-x")
	if err := p.State.Set("COD-1581", "BRANCH", "feature/COD-1581-x"); err != nil {
		t.Fatal(err)
	}

	if err := p.prepareWorktree(context.Background(), "COD-1581"); err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	branch, err := p.Git.CurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("current branch: %v", err)
	}
	if branch != "feature/COD-1581-x" {
		t.Errorf("tree is on %q, want the ticket's recorded branch", branch)
	}
}

// TestPrepareWorktreeParksWhenTheBranchIsHeldElsewhere: git allows a branch one
// working tree, so a branch the user's own checkout holds is a blameless park
// naming that tree — never a cryptic git refusal.
func TestPrepareWorktreeParksWhenTheBranchIsHeldElsewhere(t *testing.T) {
	p, root, _ := worktreeRepo(t)
	gitRun(t, root, "checkout", "-b", "feature/COD-1581-x")
	if err := p.State.Set("COD-1581", "BRANCH", "feature/COD-1581-x"); err != nil {
		t.Fatal(err)
	}

	err := p.prepareWorktree(context.Background(), "COD-1581")
	if !IsPaused(err) {
		t.Fatalf("err = %v, want a blameless park", err)
	}
	if got := p.State.Get("COD-1581", "FAILURE_CLASS"); got != state.FailPaused {
		t.Errorf("FAILURE_CLASS = %q, want %q", got, state.FailPaused)
	}
	if reason := p.State.Get("COD-1581", "FAILURE_REASON"); !strings.Contains(reason, root) {
		t.Errorf("FAILURE_REASON = %q, want it to name the tree holding the branch", reason)
	}
}

// TestPrepareWorktreeFaultsOnSetupFailureAndKeepsTheTree: the command's output is
// the only account of why provisioning stopped, so it is kept as an artifact — and
// so is the tree it failed in.
func TestPrepareWorktreeFaultsOnSetupFailureAndKeepsTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the setup command runs under sh")
	}
	p, _, dir := worktreeRepo(t)
	p.WorktreeSetupCmd = "echo 'npm ci exploded'; exit 7"
	artifacts := &recordingArtifacts{}
	p.Artifacts = artifacts

	err := p.prepareWorktree(context.Background(), "COD-1581")
	if !IsFault(err) {
		t.Fatalf("err = %v, want a fault", err)
	}
	if got := p.State.Get("COD-1581", "FAILURE_CLASS"); got != state.FailFaulted {
		t.Errorf("FAILURE_CLASS = %q, want %q", got, state.FailFaulted)
	}
	if body := artifacts.kinds[artifactWorktreeSetup]; !strings.Contains(body, "npm ci exploded") {
		t.Errorf("worktree-setup artifact = %q, want the command's output", body)
	}
	tree := filepath.Join(dir, "shipflock", "COD-1581")
	if _, statErr := os.Stat(tree); statErr != nil {
		t.Errorf("the tree was not kept for inspection: %v", statErr)
	}
}

// TestSettleTicketWorktreeRemovesTheTreeAndPointsTheRunBack is what every settle
// path — merge, reset, requeue, purge — leaves behind: no tree, no git record of
// one, a run pointed back at the registered root, and the hub told.
func TestSettleTicketWorktreeRemovesTheTreeAndPointsTheRunBack(t *testing.T) {
	p, root, dir := worktreeRepo(t)
	var settled [2]string
	p.SettleWorktree = func(_ context.Context, ticket, path string) error {
		settled = [2]string{ticket, path}
		return nil
	}
	if err := p.prepareWorktree(context.Background(), "COD-1581"); err != nil {
		t.Fatalf("prepareWorktree: %v", err)
	}
	tree := filepath.Join(dir, "shipflock", "COD-1581")

	p.settleTicketWorktree(context.Background(), "COD-1581")

	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Errorf("the tree survived the settle (err = %v)", err)
	}
	if p.WorkTree != "" {
		t.Errorf("WorkTree = %q, want the run pointed back at the registered root", p.WorkTree)
	}
	if got := p.Git.(ExecGit).Repo; got != root {
		t.Errorf("Git targets %q, want the registered root %q", got, root)
	}
	if settled != [2]string{"COD-1581", tree} {
		t.Errorf("settled %v, want the ticket and its tree", settled)
	}
	// Idempotent: a settle that finds nothing left is silent.
	p.settleTicketWorktree(context.Background(), "COD-1581")
}

// recordingArtifacts is an in-memory ArtifactStore that keeps the last body per
// kind.
type recordingArtifacts struct{ kinds map[string]string }

func (a *recordingArtifacts) Put(_, kind, content string) error {
	if a.kinds == nil {
		a.kinds = map[string]string{}
	}
	a.kinds[kind] = content
	return nil
}
func (a *recordingArtifacts) Get(_, kind string) (string, bool, error) {
	body, ok := a.kinds[kind]
	return body, ok, nil
}
func (a *recordingArtifacts) Remove(string) error { return nil }
