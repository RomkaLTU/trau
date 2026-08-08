package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
