package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureRepoConfigInclude exercises the .gitconfig.repo wiring against a
// real repo: absent file → no-op; present file → include.path added exactly
// once (idempotent), and git then resolves user.email from the repo-pinned
// file, which is the whole point — commits can no longer pick up a stray
// identity from the developer's global config.
func TestEnsureRepoConfigInclude(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init")

	added, err := EnsureRepoConfigInclude(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("absent file: %v", err)
	}
	if added {
		t.Fatal("added include without a .gitconfig.repo present")
	}

	pinned := "[user]\n\tname = Pinned\n\temail = pinned@example.com\n"
	if err := os.WriteFile(filepath.Join(dir, RepoConfigFile), []byte(pinned), 0o644); err != nil {
		t.Fatal(err)
	}

	added, err = EnsureRepoConfigInclude(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("first wire: %v", err)
	}
	if !added {
		t.Fatal("first call did not add the include")
	}

	added, err = EnsureRepoConfigInclude(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("second wire: %v", err)
	}
	if added {
		t.Fatal("second call added a duplicate include")
	}

	out, err := exec.Command("git", "-C", dir, "config", "user.email").Output()
	if err != nil {
		t.Fatalf("resolve user.email: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "pinned@example.com" {
		t.Fatalf("user.email = %q, want pinned@example.com (repo-pinned file not respected)", got)
	}
}

// TestEnsureChildConfigIncludeFallsBackToTheFolderRoot is the Folder repo identity
// grain: a child with its own .gitconfig.repo commits under that, and a child
// without one resolves the folder root's — the file the folder root has no git
// config of its own to wire in.
func TestEnsureChildConfigIncludeFallsBackToTheFolderRoot(t *testing.T) {
	root := t.TempDir()
	pinned := func(dir, email string) {
		t.Helper()
		body := "[user]\n\tname = Pinned\n\temail = " + email + "\n"
		if err := os.WriteFile(filepath.Join(dir, RepoConfigFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pinned(root, "folder@example.com")

	own := filepath.Join(root, "api-companies")
	inherits := filepath.Join(root, "api-billing")
	for _, child := range []string{own, inherits} {
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, child, "init")
	}
	pinned(own, "child@example.com")

	for child, want := range map[string]string{own: "child@example.com", inherits: "folder@example.com"} {
		added, err := EnsureChildConfigInclude(context.Background(), root, child)
		if err != nil {
			t.Fatalf("wire %s: %v", child, err)
		}
		if !added {
			t.Fatalf("%s: no include added", child)
		}
		out, err := exec.Command("git", "-C", child, "config", "user.email").Output()
		if err != nil {
			t.Fatalf("resolve user.email in %s: %v", child, err)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%s user.email = %q, want %q", filepath.Base(child), got, want)
		}
	}
}

// TestEnsureChildConfigIncludeRefusesAConfigGitCannotRead is the wedge guard: an
// include.path pointing at a file git cannot parse fails every later git call in
// that child — the unset that would remove the include again included — so a
// malformed file is refused with the child's git left exactly as usable as before.
func TestEnsureChildConfigIncludeRefusesAConfigGitCannotRead(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "api-users")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, child, "init")
	if err := os.WriteFile(filepath.Join(child, RepoConfigFile), []byte("this is not a git config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureChildConfigInclude(context.Background(), root, child); err == nil {
		t.Fatal("wired a .gitconfig.repo git cannot read")
	}
	if err := exec.Command("git", "-C", child, "status", "--porcelain").Run(); err != nil {
		t.Fatalf("child git is unusable after the refusal: %v", err)
	}
}
