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

	added, err := EnsureRepoConfigInclude(context.Background(), dir)
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

	added, err = EnsureRepoConfigInclude(context.Background(), dir)
	if err != nil {
		t.Fatalf("first wire: %v", err)
	}
	if !added {
		t.Fatal("first call did not add the include")
	}

	added, err = EnsureRepoConfigInclude(context.Background(), dir)
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
