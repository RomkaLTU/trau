package webserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// folderDiffFixture builds a hub over a real Folder repo: a directory that is not
// a git repository itself, holding two that are. Each child starts on main with one
// committed file, and the folder root is what the hub has registered.
func folderDiffFixture(t *testing.T) (*httptest.Server, *Server, string, func(child string, args ...string)) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	root := filepath.Join(t.TempDir(), "acme")
	git := func(child string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", filepath.Join(root, child)}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", child, args, err, out)
		}
	}
	for _, child := range []string{"api-companies", "api-billing"} {
		if err := os.MkdirAll(filepath.Join(root, child), 0o755); err != nil {
			t.Fatalf("mkdir child: %v", err)
		}
		git(child, "init", "-b", "main")
		writeRepoFile(t, filepath.Join(root, child), "service.go", "package main\n")
		git(child, "add", "-A")
		git(child, "commit", "-m", "init")
	}
	if err := s.stores.Registrations().Register(root); err != nil {
		t.Fatalf("register folder repo: %v", err)
	}
	return ts, s, root, git
}

// TestRunDiffFolderShowsLooseWorkBeforeShip covers the whole build and verify
// window of a folder run: the branch is cut only at ship time, so every child the
// run has changed still carries its work loose on the base branch and the pane has
// to render all of them from the working trees.
func TestRunDiffFolderShowsLooseWorkBeforeShip(t *testing.T) {
	ts, s, root, _ := folderDiffFixture(t)
	for _, child := range []string{"api-companies", "api-billing"} {
		writeRepoFile(t, filepath.Join(root, child), "service.go", "package main\n\nfunc New() {}\n")
	}
	seedDiffCheckpoint(t, s, root, "COD-3", map[string]string{
		"BRANCH": "feature/COD-3-x",
		"BASE":   "main",
	})

	status, diff := getRunDiff(t, ts, "COD-3")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if diff.Source != "live" {
		t.Errorf("source = %q, want live — nothing is committed yet", diff.Source)
	}
	for _, child := range []string{"api-companies", "api-billing"} {
		f := diffFileFor(t, diff, child+"/service.go")
		if f.Repo != child || f.Additions == 0 || f.Patch == "" {
			t.Errorf("%s = repo %q, %d additions, patch %q, want its uncommitted change", f.Path, f.Repo, f.Additions, f.Patch)
		}
	}
}

// TestRunDiffFolderMergesEveryShippedChild is the Folder repo diff contract: one
// response carries every child the run shipped to, each file rooted under the
// child it came from and naming it, and neither SHA claims to speak for all of them.
func TestRunDiffFolderMergesEveryShippedChild(t *testing.T) {
	ts, s, root, git := folderDiffFixture(t)
	for _, child := range []string{"api-companies", "api-billing"} {
		git(child, "checkout", "-b", "feature/COD-2-x")
		writeRepoFile(t, filepath.Join(root, child), "service.go", "package main\n\nfunc New() {}\n")
		git(child, "commit", "-am", "slice")
		git(child, "checkout", "main")
	}
	seedDiffCheckpoint(t, s, root, "COD-2", map[string]string{
		"BRANCH":       "feature/COD-2-x",
		"BASE":         "main",
		"SHIP_TARGETS": "api-companies,api-billing",
	})

	status, diff := getRunDiff(t, ts, "COD-2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if diff.Base != "main" || diff.Branch != "feature/COD-2-x" {
		t.Errorf("base/branch = %q/%q, want main/feature/COD-2-x", diff.Base, diff.Branch)
	}
	if diff.HeadSHA != "" {
		t.Errorf("head_sha = %q, want it empty across several repositories", diff.HeadSHA)
	}
	for _, child := range []string{"api-companies", "api-billing"} {
		f := diffFileFor(t, diff, child+"/service.go")
		if f.Repo != child {
			t.Errorf("%s repo = %q, want %q", f.Path, f.Repo, child)
		}
		if f.Additions == 0 || f.Patch == "" {
			t.Errorf("%s carries %d additions and patch %q, want its change", f.Path, f.Additions, f.Patch)
		}
	}
}
