package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
)

// TestCheckRepoRootPassesAFolderAndNamesItsOffLimitsChildren is the Folder repo
// preflight: the folder root passes the repo check with its child count instead of
// failing for having no git of its own, and the child repos row names the children
// a run would leave alone — a warning, never a failure, since a dirty child does
// not abort a run. The children a run moves back onto their base are a separate
// list: api-web is clean and merely parked on spike, while api-billing is dirty and
// stays on wip however far off base that is.
func TestCheckRepoRootPassesAFolderAndNamesItsOffLimitsChildren(t *testing.T) {
	root := filepath.Join(t.TempDir(), "PortalPro")
	git := func(child string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", filepath.Join(root, child)}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	for _, child := range []string{"api-companies", "api-billing", "api-web"} {
		if err := os.MkdirAll(filepath.Join(root, child), 0o755); err != nil {
			t.Fatal(err)
		}
		git(child, "init", "-q", "-b", "main")
		git(child, "commit", "-q", "--allow-empty", "-m", "init")
	}
	git("api-billing", "checkout", "-q", "-b", "wip")
	git("api-web", "checkout", "-q", "-b", "spike")
	if err := os.WriteFile(filepath.Join(root, "api-billing", "scratch.md"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := newTestRunner()
	if !checkRepoRoot(context.Background(), config.Config{BaseBranch: "main", Remote: "origin"}, root, rr) {
		t.Fatal("checkRepoRoot rejected a folder repo")
	}

	checks := map[string]Check{}
	for _, c := range rr.r.Checks {
		checks[c.Name] = c
	}
	repo := checks["repo"]
	if repo.Status != pass || !strings.Contains(repo.Message, "folder repo, 3 child repos") {
		t.Errorf("repo check = %s %q, want a pass naming the child count", repo.Status, repo.Message)
	}
	children := checks["child repos"]
	if children.Status != warn {
		t.Errorf("child repos status = %q, want a warning — a dirty child never fails the check", children.Status)
	}
	if !strings.Contains(children.Message, "api-billing (it has uncommitted changes)") {
		t.Errorf("child repos message = %q, want api-billing named off limits", children.Message)
	}
	if !strings.Contains(children.Message, "2 of 3 shippable, each to its own base") {
		t.Errorf("child repos message = %q, want the ready count", children.Message)
	}
	if !strings.Contains(children.Message, "parked off base (a run moves these back): api-web on spike, base main") {
		t.Errorf("child repos message = %q, want the clean parked child named as one a run moves back", children.Message)
	}
	if strings.Contains(children.Message, "api-billing on wip") {
		t.Errorf("child repos message = %q, want the dirty child left out of the parked list — a run never moves it", children.Message)
	}
}
