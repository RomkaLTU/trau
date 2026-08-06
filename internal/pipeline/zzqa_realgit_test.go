package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func qaGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// qaRealFolder builds a folder of real git repos, each with a real bare remote
// already carrying main.
func qaRealFolder(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	remotes := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		bare := filepath.Join(remotes, name+".git")
		qaGit(t, remotes, "init", "--bare", "-b", "main", bare)
		qaGit(t, root, "init", "-b", "main", dir)
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		qaGit(t, dir, "add", "-A")
		qaGit(t, dir, "commit", "-m", "init")
		qaGit(t, dir, "remote", "add", "origin", bare)
		qaGit(t, dir, "push", "-u", "origin", "main")
	}
	return root
}

// TestQARealGitFanOutAndResume drives the whole crash-safety contract over real git
// repositories — no fake Git seam — for a run killed after the first child's PR and
// resumed: the fork points are the genuine commits each branch was cut at, nothing
// is recommitted or rebranched, and the untouched child stays untouched.
func TestQARealGitFanOutAndResume(t *testing.T) {
	id, branch := "COD-99100", "feature/COD-99100-x"
	root := qaRealFolder(t, "api-a", "api-b", "api-idle")
	mains := map[string]string{}
	for _, name := range []string{"api-a", "api-b", "api-idle"} {
		mains[name] = qaGit(t, filepath.Join(root, name), "rev-parse", "HEAD")
	}

	ghs := map[string]*epicGitHub{
		"api-a":    {createURL: "https://github.com/acme/api-a/pull/1"},
		"api-b":    {createErr: errors.New("gh killed mid fan-out")},
		"api-idle": {},
	}
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	// the build dirties two children, leaving api-idle alone
	for _, name := range []string{"api-a", "api-b"} {
		if err := os.WriteFile(filepath.Join(root, name, "slice.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.CommitAndPR(ctx, id); err == nil {
		t.Fatal("want the first attempt to die on api-b's PR create")
	}

	// what the killed attempt left behind
	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a,api-b" {
		t.Fatalf("SHIP_TARGETS after the kill = %q, want api-a,api-b", got)
	}
	forks := p.State.Get(id, "FORK_POINTS")
	want := "api-a=" + mains["api-a"] + ",api-b=" + mains["api-b"]
	if forks != want {
		t.Errorf("FORK_POINTS = %q, want the real commits the branches were cut at %q", forks, want)
	}
	if got := p.State.Get(id, "PR_URLS"); got != "api-a=https://github.com/acme/api-a/pull/1" {
		t.Errorf("PR_URLS after the kill = %q, want api-a's PR already stamped", got)
	}
	aHead := qaGit(t, filepath.Join(root, "api-a"), "rev-parse", branch)
	bHead := qaGit(t, filepath.Join(root, "api-b"), "rev-parse", branch)
	for _, name := range []string{"api-a", "api-b"} {
		if n := qaGit(t, filepath.Join(root, name), "rev-list", "--count", "main.."+branch); n != "1" {
			t.Errorf("%s has %s commits on %s, want 1", name, n, branch)
		}
	}
	if out := qaGit(t, filepath.Join(root, "api-idle"), "branch", "--list"); strings.Contains(out, "COD-99100") {
		t.Errorf("api-idle got the ticket's branch: %s", out)
	}

	// --- resume the same attempt ---
	// the failed create left no PR on the forge, so its call is not a real one
	ghs["api-b"].createCalls = 0
	ghs["api-b"].createErr = nil
	ghs["api-b"].createURL = "https://github.com/acme/api-b/pull/2"
	p2 := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p2.State = p.State
	p2.FolderRepo = true
	p2.RepoRoot = root
	p2.Remote = "origin"
	p2.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	p2.startFolderRun(ctx, id, true)

	if err := p2.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("resume: %v", err)
	}

	if got := p2.State.Get(id, "SHIP_TARGETS"); got != "api-a,api-b" {
		t.Errorf("SHIP_TARGETS after resume = %q", got)
	}
	if got := p2.State.Get(id, "FORK_POINTS"); got != want {
		t.Errorf("FORK_POINTS after resume = %q, want the pins unchanged %q", got, want)
	}
	if got := qaGit(t, filepath.Join(root, "api-a"), "rev-parse", branch); got != aHead {
		t.Errorf("api-a's branch moved on resume: %s -> %s", aHead, got)
	}
	if got := qaGit(t, filepath.Join(root, "api-b"), "rev-parse", branch); got != bHead {
		t.Errorf("api-b's branch moved on resume: %s -> %s", bHead, got)
	}
	if ghs["api-a"].createCalls != 1 {
		t.Errorf("api-a's PR was created %d times across both attempts, want 1", ghs["api-a"].createCalls)
	}
	if ghs["api-b"].createCalls != 1 {
		t.Errorf("api-b's PR was created %d times, want 1", ghs["api-b"].createCalls)
	}
	if ghs["api-idle"].createCalls != 0 {
		t.Errorf("api-idle opened a PR")
	}
	if got := p2.State.Get(id, "PR_URL"); got != "https://github.com/acme/api-a/pull/1" {
		t.Errorf("PR_URL = %q, want the first target's", got)
	}
	if got := p2.State.Get(id, "PHASE"); got != "pr_open" {
		t.Errorf("PHASE = %q, want pr_open", got)
	}
	for name, gh := range ghs {
		if name == "api-idle" {
			continue
		}
		edited := gh.bodyEdits[prNumber(gh.createURL)]
		if !strings.Contains(edited, "## Ships with") {
			t.Errorf("%s body was not cross-linked:\n%s", name, edited)
		}
		if strings.Contains(edited, "- "+name+":") {
			t.Errorf("%s lists itself:\n%s", name, edited)
		}
	}
}

// TestQARealGitFreshRunIgnoresAnAbandonedBranch is fail condition 2 over real git:
// an abandoned attempt left the ticket's branch (and a commit) in api-b, and the new
// build only touched api-a. api-b must not be re-branched, pushed or PR'd.
func TestQARealGitFreshRunIgnoresAnAbandonedBranch(t *testing.T) {
	id, branch := "COD-99101", "feature/COD-99101-x"
	root := qaRealFolder(t, "api-a", "api-b")

	// the abandoned attempt's leftovers in api-b
	bDir := filepath.Join(root, "api-b")
	qaGit(t, bDir, "checkout", "-b", branch)
	if err := os.WriteFile(filepath.Join(bDir, "abandoned.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	qaGit(t, bDir, "add", "-A")
	qaGit(t, bDir, "commit", "-m", "abandoned attempt")
	abandoned := qaGit(t, bDir, "rev-parse", "HEAD")
	qaGit(t, bDir, "checkout", "main")

	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/1"},
		"api-b": {createURL: "https://github.com/acme/api-b/pull/2"},
	}
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"BRANCH":       branch,
		"SHIP_TARGETS": "api-b",
		"PR_URLS":      "api-b=https://github.com/acme/api-b/pull/99",
		"FORK_POINTS":  "api-b=deadbeef",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	if err := os.WriteFile(filepath.Join(root, "api-a", "slice.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}

	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a" {
		t.Errorf("SHIP_TARGETS = %q, want only api-a — api-b's branch is an abandoned attempt's", got)
	}
	if ghs["api-b"].createCalls != 0 {
		t.Errorf("api-b opened %d PRs off an abandoned branch", ghs["api-b"].createCalls)
	}
	if got := qaGit(t, bDir, "rev-parse", branch); got != abandoned {
		t.Errorf("api-b's abandoned branch moved: %s -> %s", abandoned, got)
	}
	if got := qaGit(t, bDir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("api-b was checked out onto %q", got)
	}
	// api-b must not have been pushed to its remote either
	if out := qaGit(t, bDir, "ls-remote", "--heads", "origin"); strings.Contains(out, branch) {
		t.Errorf("api-b's abandoned branch reached the remote:\n%s", out)
	}
	if strings.Contains(ghs["api-a"].body, "Ships with") || len(ghs["api-a"].bodyEdits) > 0 {
		t.Errorf("a single-child ship carries plural chrome: %q %v", ghs["api-a"].body, ghs["api-a"].bodyEdits)
	}
}
