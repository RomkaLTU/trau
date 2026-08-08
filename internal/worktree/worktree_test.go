package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(gitPinnedMain(m)) }

// gitPinnedMain pins git's configuration for the whole binary: these tests hand
// real repositories to production code, and without a pin the identity comes from
// the developer's own ~/.gitconfig — absent on every CI runner.
func gitPinnedMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "trau-worktree-gitconfig")
	if err != nil {
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "gitconfig")
	cfg := "[user]\n\tname = trau-test\n\temail = trau-test@example.invalid\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		return 1
	}
	_ = os.Setenv("GIT_CONFIG_GLOBAL", path)
	_ = os.Setenv("GIT_CONFIG_SYSTEM", path)
	return m.Run()
}

// repoFixture builds a one-commit git repository with a .gitignore that ignores
// the dotenv files, plus the trau files a tree is supposed to inherit.
func repoFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "shipflock")
	mustMkdir(t, root)
	git(t, root, "init")
	write(t, filepath.Join(root, ".gitignore"), ".env\n.env.*\n.trau/\n")
	write(t, filepath.Join(root, "README.md"), "hello\n")
	write(t, filepath.Join(root, ".env"), "SECRET=1\n")
	write(t, filepath.Join(root, ".env.local"), "SECRET=2\n")
	write(t, filepath.Join(root, "tracked.env"), "NOT SECRET\n")
	write(t, filepath.Join(root, ".trau.ini"), "PROJECT=trau\n")
	write(t, filepath.Join(root, ".gitconfig.repo"), "[user]\n\temail = repo@example.invalid\n")
	write(t, filepath.Join(root, ".trau", "checks", "smoke.json"), "{}\n")
	write(t, filepath.Join(root, ".trau", "runs", "COD-1", "state"), "PHASE=merged\n")
	write(t, filepath.Join(root, ".agents", "skills", "one.md"), "skill\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "init")
	return root
}

func TestPathIsDeterministicAndCannotClimbOut(t *testing.T) {
	tests := []struct {
		name            string
		dir, repo, tick string
		want            string
	}{
		{name: "plain", dir: "/w", repo: "shipflock", tick: "COD-1581", want: filepath.Join("/w", "shipflock", "COD-1581")},
		{name: "repo given as a root path", dir: "/w", repo: "/src/shipflock", tick: "COD-1", want: filepath.Join("/w", "shipflock", "COD-1")},
		{name: "ticket with separators", dir: "/w", repo: "acme", tick: "../../etc", want: filepath.Join("/w", "acme", "etc")},
		{name: "no dir", dir: "", repo: "acme", tick: "COD-1", want: ""},
		{name: "no ticket", dir: "/w", repo: "acme", tick: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Path(tt.dir, tt.repo, tt.tick); got != tt.want {
				t.Errorf("Path = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestValidateDirRefusesADirectoryInsideARepo pins the repo-identity rule: a tree
// inside a checkout is part of the very checkout it exists to stay out of.
func TestValidateDirRefusesADirectoryInsideARepo(t *testing.T) {
	root := repoFixture(t)

	if err := ValidateDir(filepath.Join(root, "worktrees")); err == nil {
		t.Fatal("a worktrees dir inside a repo was accepted")
	} else if !strings.Contains(err.Error(), root) {
		t.Errorf("refusal = %v, want it to name the repo %s", err, root)
	}
	if err := ValidateDir(filepath.Join(t.TempDir(), "worktrees")); err != nil {
		t.Errorf("a dir outside every repo was refused: %v", err)
	}
	if err := ValidateDir(""); err == nil {
		t.Error("an empty worktrees dir was accepted")
	}
	if err := ValidateDir("relative/worktrees"); err == nil {
		t.Error("a relative worktrees dir was accepted")
	}
}

// TestProvisionCreatesADetachedTreeAndCopiesTheIgnoredFiles is the fresh path:
// the tree starts detached at the base tip with the branch still to be cut, and it
// arrives holding exactly what git cannot carry — the gitignored dotenvs, trau's
// own files — and nothing tracked, nothing from another run's RUNS_DIR.
func TestProvisionCreatesADetachedTreeAndCopiesTheIgnoredFiles(t *testing.T) {
	root := repoFixture(t)
	dir := filepath.Join(t.TempDir(), "worktrees")

	res, err := Provision(context.Background(), Options{
		RepoRoot: root,
		Dir:      dir,
		Repo:     "shipflock",
		Ticket:   "COD-1581",
		Base:     "main",
		Copy:     []string{".env", ".env.*", "tracked.env", "nothing-matches-*"},
		RunsDir:  ".trau/runs",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !res.Created {
		t.Error("Created = false, want a fresh tree")
	}
	if want := filepath.Join(dir, "shipflock", "COD-1581"); res.Path != want {
		t.Fatalf("Path = %q, want %q", res.Path, want)
	}
	if res.Branch != "" {
		t.Errorf("Branch = %q, want empty (detached until the branch is cut inside)", res.Branch)
	}

	for _, rel := range []string{
		"README.md", ".env", ".env.local", ".trau.ini", ".gitconfig.repo",
		filepath.Join(".trau", "checks", "smoke.json"),
		filepath.Join(".agents", "skills", "one.md"),
	} {
		if _, err := os.Stat(filepath.Join(res.Path, rel)); err != nil {
			t.Errorf("%s missing from the tree: %v", rel, err)
		}
	}
	// The run artifacts of another ticket must not ride along.
	if _, err := os.Stat(filepath.Join(res.Path, ".trau", "runs", "COD-1", "state")); !os.IsNotExist(err) {
		t.Errorf("RUNS_DIR contents were copied into the tree (err = %v)", err)
	}
	// A tracked file matched by a glob is already in the tree at the commit it
	// holds; copying the root's copy over it would shadow that commit.
	if got := read(t, filepath.Join(res.Path, "tracked.env")); got != "NOT SECRET\n" {
		t.Errorf("tracked.env = %q, want the committed content", got)
	}
}

// TestProvisionAdoptsAnExistingTreeWithItsWIP is the resume path a crash leaves:
// the tree is reused, its uncommitted work survives, and the copy step does not run
// again over files the run has since changed.
func TestProvisionAdoptsAnExistingTreeWithItsWIP(t *testing.T) {
	root := repoFixture(t)
	dir := filepath.Join(t.TempDir(), "worktrees")
	opts := Options{
		RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581",
		Base: "main", Copy: []string{".env"}, RunsDir: ".trau/runs",
	}

	first, err := Provision(context.Background(), opts)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	write(t, filepath.Join(first.Path, ".env"), "EDITED BY THE RUN\n")
	write(t, filepath.Join(first.Path, "wip.txt"), "half-finished\n")

	second, err := Provision(context.Background(), opts)
	if err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if second.Created {
		t.Error("Created = true, want the existing tree adopted")
	}
	if second.Path != first.Path {
		t.Errorf("Path = %q, want the same tree %q", second.Path, first.Path)
	}
	if got := read(t, filepath.Join(second.Path, ".env")); got != "EDITED BY THE RUN\n" {
		t.Errorf(".env = %q, want the adopted tree's own copy left alone", got)
	}
	if _, err := os.Stat(filepath.Join(second.Path, "wip.txt")); err != nil {
		t.Errorf("the adopted tree lost its WIP: %v", err)
	}
}

// TestProvisionTakesTheTicketBranchWhenItExists is the other half of resume: a
// branch already cut is checked out in the tree rather than left to be re-cut.
func TestProvisionTakesTheTicketBranchWhenItExists(t *testing.T) {
	root := repoFixture(t)
	git(t, root, "branch", "feature/COD-1581-x")
	dir := filepath.Join(t.TempDir(), "worktrees")

	res, err := Provision(context.Background(), Options{
		RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581",
		Branch: "feature/COD-1581-x", Base: "main",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if res.Branch != "feature/COD-1581-x" {
		t.Errorf("Branch = %q, want the ticket's existing branch", res.Branch)
	}
	if got := gitOut(t, res.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/COD-1581-x" {
		t.Errorf("tree HEAD = %q, want feature/COD-1581-x", got)
	}
}

// TestProvisionParksWhenTheBranchIsHeldElsewhere covers the structural conflict:
// git allows a branch one working tree, so a branch the user's own checkout holds
// is a park naming that tree — never git's refusal from three layers down.
func TestProvisionParksWhenTheBranchIsHeldElsewhere(t *testing.T) {
	root := repoFixture(t)
	git(t, root, "checkout", "-b", "feature/COD-1581-x")
	dir := filepath.Join(t.TempDir(), "worktrees")

	_, err := Provision(context.Background(), Options{
		RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581",
		Branch: "feature/COD-1581-x", Base: "main",
	})
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want a *HeldError", err)
	}
	if held.Branch != "feature/COD-1581-x" {
		t.Errorf("HeldError.Branch = %q", held.Branch)
	}
	if !strings.Contains(held.Error(), "one working tree at a time") {
		t.Errorf("HeldError = %q, want it to explain the git rule", held.Error())
	}
}

// TestProvisionSetupFailureKeepsTheTree pins the contract the operator depends on:
// the command's output is carried back, and the tree it failed in is still there to
// read it next to.
func TestProvisionSetupFailureKeepsTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the setup command runs under sh")
	}
	root := repoFixture(t)
	dir := filepath.Join(t.TempDir(), "worktrees")

	res, err := Provision(context.Background(), Options{
		RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581",
		Base: "main", SetupCmd: "echo 'install exploded' >&2; exit 3",
	})
	var setup *SetupError
	if !errors.As(err, &setup) {
		t.Fatalf("err = %v, want a *SetupError", err)
	}
	if !strings.Contains(setup.Output, "install exploded") {
		t.Errorf("SetupError.Output = %q, want the command's output", setup.Output)
	}
	if res.Path == "" {
		t.Fatal("Result carried no path, so the caller cannot report the kept tree")
	}
	if _, statErr := os.Stat(res.Path); statErr != nil {
		t.Errorf("the tree was not kept for inspection: %v", statErr)
	}
}

// TestProvisionRunsTheSetupCommandInTheFreshTree pins where the command runs: the
// tree, not the registered root.
func TestProvisionRunsTheSetupCommandInTheFreshTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the setup command runs under sh")
	}
	root := repoFixture(t)
	dir := filepath.Join(t.TempDir(), "worktrees")

	res, err := Provision(context.Background(), Options{
		RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581",
		Base: "main", SetupCmd: "pwd > setup-ran-here.txt",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	got := strings.TrimSpace(read(t, filepath.Join(res.Path, "setup-ran-here.txt")))
	if resolved, err := filepath.EvalSymlinks(res.Path); err == nil && got != resolved && got != res.Path {
		t.Errorf("setup ran in %q, want the fresh tree %q", got, res.Path)
	}
}

// TestRemoveTakesTheTreeOffDiskAndOutOfGitsRegistry is the settle side, including
// its idempotence: a second removal of a tree already gone is not an error.
func TestRemoveTakesTheTreeOffDiskAndOutOfGitsRegistry(t *testing.T) {
	root := repoFixture(t)
	dir := filepath.Join(t.TempDir(), "worktrees")
	res, err := Provision(context.Background(), Options{
		RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581", Base: "main",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	// Dirty the tree: settle must not be gated on a clean one.
	write(t, filepath.Join(res.Path, "leftover.txt"), "x\n")

	if err := Remove(context.Background(), root, res.Path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Errorf("the tree survived removal (err = %v)", err)
	}
	entries, err := List(context.Background(), root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range entries {
		if samePath(e.Path, res.Path) {
			t.Errorf("git still lists the removed tree %s", e.Path)
		}
	}
	if err := Remove(context.Background(), root, res.Path); err != nil {
		t.Errorf("removing an already-removed tree = %v, want nil", err)
	}
}

// TestProvisionAdoptsAfterTheDirectoryWasDeletedByHand covers the crash-and-clean
// case: git's registry still names a tree the disk lost, and provisioning has to
// prune that record rather than refuse the path forever.
func TestProvisionAdoptsAfterTheDirectoryWasDeletedByHand(t *testing.T) {
	root := repoFixture(t)
	dir := filepath.Join(t.TempDir(), "worktrees")
	opts := Options{RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581", Base: "main"}

	res, err := Provision(context.Background(), opts)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := os.RemoveAll(res.Path); err != nil {
		t.Fatal(err)
	}

	again, err := Provision(context.Background(), opts)
	if err != nil {
		t.Fatalf("re-provision after a hand-deleted tree: %v", err)
	}
	if !again.Created {
		t.Error("Created = false, want a fresh tree after the old one vanished")
	}
	if _, err := os.Stat(filepath.Join(again.Path, "README.md")); err != nil {
		t.Errorf("the re-created tree has no checkout: %v", err)
	}
}

func TestListReportsBranchesAndDetachment(t *testing.T) {
	root := repoFixture(t)
	dir := filepath.Join(t.TempDir(), "worktrees")
	res, err := Provision(context.Background(), Options{
		RepoRoot: root, Dir: dir, Repo: "shipflock", Ticket: "COD-1581", Base: "main",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	entries, err := List(context.Background(), root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var main, tree *Entry
	for i := range entries {
		switch {
		case samePath(entries[i].Path, root):
			main = &entries[i]
		case samePath(entries[i].Path, res.Path):
			tree = &entries[i]
		}
	}
	if main == nil || main.Branch != "main" {
		t.Fatalf("main checkout entry = %+v, want branch main", main)
	}
	if tree == nil || !tree.Detached || tree.Branch != "" {
		t.Fatalf("worktree entry = %+v, want detached with no branch", tree)
	}

	holder, err := Holding(context.Background(), root, "main")
	if err != nil {
		t.Fatalf("holding: %v", err)
	}
	if !samePath(holder, root) {
		t.Errorf("Holding(main) = %q, want the registered root", holder)
	}
	if holder, err := Holding(context.Background(), root, "nobody-holds-this"); err != nil || holder != "" {
		t.Errorf("Holding(unheld) = (%q, %v), want empty", holder, err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// TestProvisionRefusesADirInsideTheRegisteredRoot covers the folder-repo half of
// the repo-identity rule: a folder root is not a git repository, so ValidateDir
// cannot see it — provisioning checks the root it was handed instead.
func TestProvisionRefusesADirInsideTheRegisteredRoot(t *testing.T) {
	folder := t.TempDir()
	inner := filepath.Join(folder, "shipflock")
	mustMkdir(t, inner)
	git(t, inner, "init")

	_, err := Provision(context.Background(), Options{
		RepoRoot: folder,
		Dir:      filepath.Join(folder, "worktrees"),
		Repo:     "shipflock",
		Ticket:   "COD-1",
	})
	if err == nil {
		t.Fatal("a worktrees dir inside the registered root was accepted")
	}
	if !strings.Contains(err.Error(), folder) {
		t.Errorf("refusal = %v, want it to name the registered root", err)
	}
}
