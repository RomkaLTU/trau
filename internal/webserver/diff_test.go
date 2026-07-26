package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// diffFixture builds a hub over a real git repository — the diff endpoint runs git
// plumbing, so a planted .git stub would not exercise it — and returns the test
// server, the repo root, and a git runner scoped to it. The repo starts on main with
// one committed file.
func diffFixture(t *testing.T) (*httptest.Server, *Server, string, func(args ...string)) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	root := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	writeRepoFile(t, root, "a.txt", "one\ntwo\n")
	writeRepoFile(t, root, "gone.txt", "doomed\n")
	git("add", "-A")
	git("commit", "-m", "init")
	if err := s.stores.Registrations().Register(root); err != nil {
		t.Fatalf("register repo: %v", err)
	}
	return ts, s, root, git
}

func writeRepoFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func seedDiffCheckpoint(t *testing.T, s *Server, root, ticket string, data map[string]string) {
	t.Helper()
	if err := s.stores.Checkpoints().Upsert(root, ticket, data); err != nil {
		t.Fatalf("seed checkpoint %s: %v", ticket, err)
	}
}

func getRunDiff(t *testing.T, ts *httptest.Server, ticket string) (int, RunDiff) {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos/acme/runs/"+ticket+"/diff")
	var diff RunDiff
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &diff); err != nil {
			t.Fatalf("decode diff: %v (body %q)", err, body)
		}
	}
	return res.StatusCode, diff
}

func diffFileFor(t *testing.T, diff RunDiff, path string) RunDiffFile {
	t.Helper()
	for _, f := range diff.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no diff entry for %s (files %+v)", path, diff.Files)
	return RunDiffFile{}
}

func TestRunDiffLiveIncludesUncommittedWork(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-1-x")
	writeRepoFile(t, root, "a.txt", "one\ntwoX\n")
	git("commit", "-am", "committed edit")
	writeRepoFile(t, root, "a.txt", "one\ntwoY\nthree\n")
	git("rm", "-q", "gone.txt")
	seedDiffCheckpoint(t, s, root, "COD-1", map[string]string{
		"BRANCH": "feature/COD-1-x",
		"BASE":   "main",
	})

	status, diff := getRunDiff(t, ts, "COD-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if diff.Source != "live" {
		t.Errorf("source = %q, want live", diff.Source)
	}
	if diff.Base != "main" || diff.Branch != "feature/COD-1-x" {
		t.Errorf("base/branch = %q/%q, want main/feature/COD-1-x", diff.Base, diff.Branch)
	}
	if diff.BaseSHA == "" || diff.HeadSHA == "" {
		t.Errorf("base_sha/head_sha = %q/%q, want both resolved", diff.BaseSHA, diff.HeadSHA)
	}
	if diff.Truncated {
		t.Error("truncated = true, want false for a small diff")
	}

	edited := diffFileFor(t, diff, "a.txt")
	if edited.Status != "modified" {
		t.Errorf("a.txt status = %q, want modified", edited.Status)
	}
	if edited.Additions != 2 || edited.Deletions != 1 {
		t.Errorf("a.txt +%d/-%d, want +2/-1", edited.Additions, edited.Deletions)
	}
	if !strings.Contains(edited.Patch, "+twoY") {
		t.Errorf("a.txt patch missing the working-tree edit: %q", edited.Patch)
	}

	if removed := diffFileFor(t, diff, "gone.txt"); removed.Status != "deleted" {
		t.Errorf("gone.txt status = %q, want deleted", removed.Status)
	}
}

func TestRunDiffLiveIncludesUntrackedFiles(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-2-x")
	writeRepoFile(t, root, "sub/new.txt", "fresh\nwork\n")
	seedDiffCheckpoint(t, s, root, "COD-2", map[string]string{
		"BRANCH": "feature/COD-2-x",
		"BASE":   "main",
	})

	status, diff := getRunDiff(t, ts, "COD-2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	created := diffFileFor(t, diff, "sub/new.txt")
	if created.Status != "added" {
		t.Errorf("status = %q, want added", created.Status)
	}
	if created.Additions != 2 || created.Deletions != 0 {
		t.Errorf("+%d/-%d, want +2/-0", created.Additions, created.Deletions)
	}
	if !strings.Contains(created.Patch, "+fresh") {
		t.Errorf("patch missing the new content: %q", created.Patch)
	}
}

func TestRunDiffLiveIgnoresCommitsBaseGainedAfterTheFork(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-9-x")
	writeRepoFile(t, root, "branchwork.txt", "slice\n")
	git("add", "-A")
	git("commit", "-m", "branch work")
	git("checkout", "main")
	writeRepoFile(t, root, "mainwork.txt", "sibling slice\n")
	git("add", "-A")
	git("commit", "-m", "base moved on")
	git("checkout", "feature/COD-9-x")
	seedDiffCheckpoint(t, s, root, "COD-9", map[string]string{
		"BRANCH": "feature/COD-9-x",
		"BASE":   "main",
	})

	status, diff := getRunDiff(t, ts, "COD-9")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if diff.Source != "live" {
		t.Fatalf("source = %q, want live", diff.Source)
	}
	for _, f := range diff.Files {
		if f.Path != "branchwork.txt" {
			t.Errorf("diff lists %s (%s), want only the run's own branchwork.txt", f.Path, f.Status)
		}
	}
	forkPoint := gitOutput(t.Context(), root, "merge-base", "main", "HEAD")
	if diff.BaseSHA != forkPoint {
		t.Errorf("base_sha = %q, want the fork point %q", diff.BaseSHA, forkPoint)
	}
}

// divergedDiffRepo cuts a run's branch onto a sibling's line of history while the
// base branch it was cut from is rebuilt on main. The two then share nothing but the
// repo's first commit, so a merge-base against that base reports the sibling's slice
// as this run's. Returns the commit the run's own work sits on.
func divergedDiffRepo(t *testing.T) (*httptest.Server, *Server, string, string) {
	t.Helper()
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "sibling")
	writeRepoFile(t, root, "sibling.txt", "another slice's work\n")
	git("add", "-A")
	git("commit", "-m", "sibling slice")
	sibling := gitOutput(t.Context(), root, "rev-parse", "HEAD")
	git("checkout", "-b", "epic/COD-13-e", "main")
	writeRepoFile(t, root, "epicwork.txt", "epic rebuilt on main\n")
	git("add", "-A")
	git("commit", "-m", "epic work")
	git("checkout", "-b", "feature/COD-13-x", "sibling")
	writeRepoFile(t, root, "runwork.txt", "the run's own slice\n")
	git("add", "-A")
	git("commit", "-m", "run work")
	return ts, s, root, sibling
}

func TestRunDiffMeasuresFromThePinnedForkPoint(t *testing.T) {
	t.Run("the pin keeps the diff to the run's own slice", func(t *testing.T) {
		ts, s, root, sibling := divergedDiffRepo(t)
		seedDiffCheckpoint(t, s, root, "COD-13", map[string]string{
			"BRANCH":   "feature/COD-13-x",
			"BASE":     "epic/COD-13-e",
			"BASE_SHA": sibling,
		})

		status, diff := getRunDiff(t, ts, "COD-13")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if diff.BaseSHA != sibling {
			t.Errorf("base_sha = %q, want the pinned fork point %q", diff.BaseSHA, sibling)
		}
		if diff.Base != "epic/COD-13-e" {
			t.Errorf("base = %q, want the branch the run was cut from", diff.Base)
		}
		if len(diff.Files) != 1 {
			t.Fatalf("files = %+v, want only the run's own runwork.txt", diff.Files)
		}
		diffFileFor(t, diff, "runwork.txt")
	})

	t.Run("a pin the branch does not contain falls back to the merge base", func(t *testing.T) {
		ts, s, root, _ := divergedDiffRepo(t)
		seedDiffCheckpoint(t, s, root, "COD-13", map[string]string{
			"BRANCH":   "feature/COD-13-x",
			"BASE":     "epic/COD-13-e",
			"BASE_SHA": gitOutput(t.Context(), root, "rev-parse", "epic/COD-13-e"),
		})

		status, diff := getRunDiff(t, ts, "COD-13")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		forkPoint := gitOutput(t.Context(), root, "merge-base", "epic/COD-13-e", "HEAD")
		if diff.BaseSHA != forkPoint {
			t.Errorf("base_sha = %q, want the merge base %q", diff.BaseSHA, forkPoint)
		}
		diffFileFor(t, diff, "sibling.txt")
	})

	t.Run("a pin whose commit is gone falls back to the merge base", func(t *testing.T) {
		ts, s, root, _ := divergedDiffRepo(t)
		seedDiffCheckpoint(t, s, root, "COD-13", map[string]string{
			"BRANCH":   "feature/COD-13-x",
			"BASE":     "epic/COD-13-e",
			"BASE_SHA": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		})

		status, diff := getRunDiff(t, ts, "COD-13")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 — a pin git cannot resolve falls back, it does not fail", status)
		}
		mergeBase := gitOutput(t.Context(), root, "merge-base", "epic/COD-13-e", "HEAD")
		if diff.BaseSHA != mergeBase {
			t.Errorf("base_sha = %q, want the merge base %q", diff.BaseSHA, mergeBase)
		}
	})

	t.Run("a mid-run rebase onto an advanced base measures from the base it landed on", func(t *testing.T) {
		ts, s, root, git := diffFixture(t)
		git("checkout", "-b", "epic/COD-14-e")
		pin := gitOutput(t.Context(), root, "rev-parse", "HEAD")
		git("checkout", "-b", "feature/COD-14-x")
		writeRepoFile(t, root, "runwork.txt", "the run's own slice\n")
		git("add", "-A")
		git("commit", "-m", "run work")
		git("checkout", "epic/COD-14-e")
		writeRepoFile(t, root, "sibling.txt", "another slice's work\n")
		git("add", "-A")
		git("commit", "-m", "sibling slice")
		git("rebase", "epic/COD-14-e", "feature/COD-14-x")
		seedDiffCheckpoint(t, s, root, "COD-14", map[string]string{
			"BRANCH":   "feature/COD-14-x",
			"BASE":     "epic/COD-14-e",
			"BASE_SHA": pin,
		})

		status, diff := getRunDiff(t, ts, "COD-14")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		advanced := gitOutput(t.Context(), root, "rev-parse", "epic/COD-14-e")
		if diff.BaseSHA != advanced {
			t.Errorf("base_sha = %q, want the advanced base %q the branch was replayed onto", diff.BaseSHA, advanced)
		}
		if len(diff.Files) != 1 {
			t.Fatalf("files = %+v, want only the run's own runwork.txt", diff.Files)
		}
		diffFileFor(t, diff, "runwork.txt")
	})
}

func TestRunDiffFallsBackToConfiguredBaseWhenRecordedBaseIsGone(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-10-x")
	writeRepoFile(t, root, "a.txt", "one\ntwoX\n")
	seedDiffCheckpoint(t, s, root, "COD-10", map[string]string{
		"BRANCH": "feature/COD-10-x",
		"BASE":   "epic/COD-9999-merged-and-deleted",
	})

	status, diff := getRunDiff(t, ts, "COD-10")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the configured base", status)
	}
	if diff.Base != "main" {
		t.Errorf("base = %q, want the configured default main", diff.Base)
	}
	if edited := diffFileFor(t, diff, "a.txt"); !strings.Contains(edited.Patch, "+twoX") {
		t.Errorf("patch missing the working-tree edit: %q", edited.Patch)
	}
}

func TestRunDiff404sWithoutAResolvableBase(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-11-x")
	git("branch", "-D", "main")
	seedDiffCheckpoint(t, s, root, "COD-11", map[string]string{"BRANCH": "feature/COD-11-x"})

	if status, _ := getRunDiff(t, ts, "COD-11"); status != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no base branch resolves", status)
	}
}

func TestRunDiffFallsBackToCommittedWhenWorktreeMovedOn(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-3-x")
	writeRepoFile(t, root, "a.txt", "one\ntwoX\n")
	git("commit", "-am", "committed edit")
	git("checkout", "main")
	writeRepoFile(t, root, "untracked-on-main.txt", "noise\n")
	seedDiffCheckpoint(t, s, root, "COD-3", map[string]string{
		"BRANCH": "feature/COD-3-x",
		"BASE":   "main",
	})

	status, diff := getRunDiff(t, ts, "COD-3")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if diff.Source != "committed" {
		t.Errorf("source = %q, want committed", diff.Source)
	}
	if len(diff.Files) != 1 {
		t.Fatalf("files = %d, want only the committed change: %+v", len(diff.Files), diff.Files)
	}
	if edited := diffFileFor(t, diff, "a.txt"); !strings.Contains(edited.Patch, "+twoX") {
		t.Errorf("patch missing the committed edit: %q", edited.Patch)
	}
}

func TestRunDiffFallsBackToConfiguredBaseWithoutBaseKey(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-4-x")
	writeRepoFile(t, root, "a.txt", "one\ntwoX\n")
	seedDiffCheckpoint(t, s, root, "COD-4", map[string]string{"BRANCH": "feature/COD-4-x"})

	status, diff := getRunDiff(t, ts, "COD-4")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if diff.Base != "main" {
		t.Errorf("base = %q, want the configured default main", diff.Base)
	}
}

func TestRunDiff404sWithoutABranch(t *testing.T) {
	ts, s, root, _ := diffFixture(t)
	seedDiffCheckpoint(t, s, root, "COD-5", map[string]string{"BRANCH": "feature/COD-5-gone"})
	seedDiffCheckpoint(t, s, root, "COD-6", map[string]string{"PHASE": "building"})

	if status, _ := getRunDiff(t, ts, "COD-5"); status != http.StatusNotFound {
		t.Errorf("pruned branch status = %d, want 404", status)
	}
	if status, _ := getRunDiff(t, ts, "COD-6"); status != http.StatusNotFound {
		t.Errorf("branchless checkpoint status = %d, want 404", status)
	}
	if status, _ := getRunDiff(t, ts, "COD-7"); status != http.StatusNotFound {
		t.Errorf("unknown ticket status = %d, want 404", status)
	}
}

func TestRunDiffTruncatesPastTheTotalPatchCap(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-8-x")
	bulk := strings.Repeat("a line of filler text\n", 4_500)
	for i := range 24 {
		writeRepoFile(t, root, fmt.Sprintf("bulk-%02d.txt", i), bulk)
	}
	git("add", "-A")
	git("commit", "-m", "bulk")
	seedDiffCheckpoint(t, s, root, "COD-8", map[string]string{
		"BRANCH": "feature/COD-8-x",
		"BASE":   "main",
	})

	status, diff := getRunDiff(t, ts, "COD-8")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !diff.Truncated {
		t.Fatal("truncated = false, want true past the total patch cap")
	}
	if first := diffFileFor(t, diff, "bulk-00.txt"); first.Patch == "" {
		t.Error("bulk-00.txt lost its patch, want the files under the cap to keep theirs")
	}
	dropped := diffFileFor(t, diff, "bulk-23.txt")
	if dropped.Patch != "" {
		t.Errorf("bulk-23.txt kept %d patch bytes past the cap", len(dropped.Patch))
	}
	if dropped.Additions != 4_500 {
		t.Errorf("bulk-23.txt additions = %d, want the stats kept at 4500", dropped.Additions)
	}
	total := 0
	for _, f := range diff.Files {
		total += len(f.Patch)
	}
	if total > maxDiffPatchBytes {
		t.Errorf("response carries %d patch bytes, want at most %d", total, maxDiffPatchBytes)
	}
}

func TestRunDiffDropsAnOversizedFilePatch(t *testing.T) {
	ts, s, root, git := diffFixture(t)
	git("checkout", "-b", "feature/COD-12-x")
	writeRepoFile(t, root, "generated.txt", strings.Repeat("a line of filler text\n", 70_000))
	writeRepoFile(t, root, "small.txt", "hand written\n")
	git("add", "-A")
	git("commit", "-m", "one huge file")
	seedDiffCheckpoint(t, s, root, "COD-12", map[string]string{
		"BRANCH": "feature/COD-12-x",
		"BASE":   "main",
	})

	status, diff := getRunDiff(t, ts, "COD-12")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !diff.Truncated {
		t.Fatal("truncated = false, want true for a file over the per-file cap")
	}
	huge := diffFileFor(t, diff, "generated.txt")
	if huge.Patch != "" {
		t.Errorf("generated.txt kept %d patch bytes, want the single-file cap to drop it", len(huge.Patch))
	}
	if huge.Additions != 70_000 {
		t.Errorf("generated.txt additions = %d, want the stats kept at 70000", huge.Additions)
	}
	if small := diffFileFor(t, diff, "small.txt"); small.Patch == "" {
		t.Error("small.txt lost its patch, want files under the cap unaffected")
	}
}
