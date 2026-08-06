package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// qaFolder wires a folder pipeline over the given children.
func qaFolder(t *testing.T, id, branch string, gits map[string]*childGit, ghs map[string]*epicGitHub) (*Pipeline, string) {
	t.Helper()
	root := t.TempDir()
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
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}
	return p, root
}

// QA AC7 / brief 8: a cross-link body edit that gh refuses must warn and let the
// run complete — never fail or park it.
func TestQACrossLinkFailureIsNonFatal(t *testing.T) {
	id, branch := "COD-99001", "feature/COD-99001-x"
	gits := map[string]*childGit{"api-a": {}, "api-b": {}}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/1", editErr: errors.New("no write access")},
		"api-b": {createURL: "https://github.com/acme/api-b/pull/2"},
	}
	p, _ := qaFolder(t, id, branch, gits, ghs)
	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-a"].status = " M a.go"
	gits["api-b"].status = " M b.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("a refused body edit must not fail the run: %v", err)
	}
	if got := p.State.Get(id, "PHASE"); got != "pr_open" {
		t.Errorf("PHASE = %q, want pr_open — the run must complete", got)
	}
	if _, ok := ghs["api-b"].bodyEdits["2"]; !ok {
		t.Errorf("api-b's body was never cross-linked after api-a's edit was refused")
	}
}

// QA AC8 / brief 9 / fail condition 6: a single-child ship gets no "Ships with"
// section anywhere.
func TestQANoShipsWithOnSingleChild(t *testing.T) {
	id, branch := "COD-99002", "feature/COD-99002-x"
	gits := map[string]*childGit{"api-a": {}, "api-b": {}}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/1"},
		"api-b": {},
	}
	p, _ := qaFolder(t, id, branch, gits, ghs)
	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-a"].status = " M a.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}
	if strings.Contains(ghs["api-a"].body, "Ships with") {
		t.Errorf("single-child PR body carries a Ships with section:\n%s", ghs["api-a"].body)
	}
	if len(ghs["api-a"].bodyEdits) != 0 {
		t.Errorf("single-child ship rewrote its body: %v", ghs["api-a"].bodyEdits)
	}
}

// QA AC5 / fail condition 3: a run killed partway through the fan-out leaves every
// child it already committed in, and every PR it already opened, on the checkpoint.
// The kill is simulated by the third child's PR create failing after two succeeded.
func TestQACheckpointStampedIncrementally(t *testing.T) {
	id, branch := "COD-99003", "feature/COD-99003-x"
	gits := map[string]*childGit{"api-a": {}, "api-b": {}, "api-c": {}}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/1"},
		"api-b": {createURL: "https://github.com/acme/api-b/pull/2"},
		"api-c": {createErr: errors.New("gh died")},
	}
	p, _ := qaFolder(t, id, branch, gits, ghs)
	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	for _, g := range gits {
		g.status = " M x.go"
	}

	if err := p.CommitAndPR(ctx, id); err == nil {
		t.Fatal("want the run to fail on api-c's PR create")
	}
	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a,api-b,api-c" {
		t.Errorf("SHIP_TARGETS = %q, want every committed child", got)
	}
	urls := p.State.Get(id, "PR_URLS")
	for _, want := range []string{"api-a=https://github.com/acme/api-a/pull/1", "api-b=https://github.com/acme/api-b/pull/2"} {
		if !strings.Contains(urls, want) {
			t.Errorf("PR_URLS = %q, want it to already carry %q", urls, want)
		}
	}
	forks := p.State.Get(id, "FORK_POINTS")
	t.Logf("FORK_POINTS after the kill = %q", forks)
}

// QA brief 2 + AC1: killed after api-a's PR opened, before api-b committed. The
// resume must reuse api-a's PR (never recreate it) and ship api-b and api-c.
func TestQAResumeReusesAnAlreadyOpenedPR(t *testing.T) {
	id, branch := "COD-99004", "feature/COD-99004-x"
	gits := map[string]*childGit{
		"api-a": {hasBranch: true, forkedAt: "a-cut"},
		"api-b": {status: " M b.go", sha: "b-cut"},
		"api-c": {status: " M c.go", sha: "c-cut"},
	}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/1"},
		"api-b": {createURL: "https://github.com/acme/api-b/pull/2"},
		"api-c": {createURL: "https://github.com/acme/api-c/pull/3"},
	}
	p, _ := qaFolder(t, id, branch, gits, ghs)
	for key, value := range map[string]string{
		"SHIP_TARGETS": "api-a",
		"PR_URLS":      "api-a=https://github.com/acme/api-a/pull/1",
		"FORK_POINTS":  "api-a=a-cut",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	p.startFolderRun(ctx, id, true)

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR after resume: %v", err)
	}
	if ghs["api-a"].createCalls != 0 {
		t.Errorf("api-a's PR was recreated %d times, want 0 — it already had one", ghs["api-a"].createCalls)
	}
	if len(gits["api-a"].commits) != 0 || len(gits["api-a"].pushed) != 0 {
		t.Errorf("api-a was recommitted %v / repushed %v", gits["api-a"].commits, gits["api-a"].pushed)
	}
	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a,api-b,api-c" {
		t.Errorf("SHIP_TARGETS = %q, want all three", got)
	}
	if got := p.State.Get(id, "PR_URL"); got != "https://github.com/acme/api-a/pull/1" {
		t.Errorf("PR_URL = %q, want the first shipped target's PR", got)
	}
	if got := p.State.Get(id, "PR"); got != "1" {
		t.Errorf("PR = %q, want 1", got)
	}
	for name, gh := range ghs {
		edited, ok := gh.bodyEdits[prNumber(gh.createURL)]
		if !ok {
			t.Errorf("%s was never cross-linked", name)
			continue
		}
		if strings.Contains(edited, "- "+name+":") {
			t.Errorf("%s lists itself among its siblings:\n%s", name, edited)
		}
		for other := range ghs {
			if other == name {
				continue
			}
			if !strings.Contains(edited, "- "+other+": "+ghs[other].createURL) {
				t.Errorf("%s body is missing sibling %s:\n%s", name, other, edited)
			}
		}
	}
}

// QA brief 3: killed after all PRs opened but before PHASE/PR/PR_URL were stamped.
// The resume must reopen nothing.
func TestQAResumeAfterAllPRsOpenReopensNothing(t *testing.T) {
	id, branch := "COD-99005", "feature/COD-99005-x"
	gits := map[string]*childGit{
		"api-a": {hasBranch: true, forkedAt: "a-cut"},
		"api-b": {hasBranch: true, forkedAt: "b-cut"},
	}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/1"},
		"api-b": {createURL: "https://github.com/acme/api-b/pull/2"},
	}
	p, _ := qaFolder(t, id, branch, gits, ghs)
	for key, value := range map[string]string{
		"SHIP_TARGETS": "api-a,api-b",
		"PR_URLS":      "api-a=https://github.com/acme/api-a/pull/1,api-b=https://github.com/acme/api-b/pull/2",
		"FORK_POINTS":  "api-a=a-cut,api-b=b-cut",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	p.startFolderRun(ctx, id, true)

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR after resume: %v", err)
	}
	for name, gh := range ghs {
		if gh.createCalls != 0 {
			t.Errorf("%s reopened its PR %d times", name, gh.createCalls)
		}
	}
	for name, g := range gits {
		if len(g.commits) != 0 || len(g.pushed) != 0 {
			t.Errorf("%s recommitted %v / repushed %v", name, g.commits, g.pushed)
		}
	}
	if got := p.State.Get(id, "PHASE"); got != "pr_open" {
		t.Errorf("PHASE = %q, want pr_open", got)
	}
	if got := p.State.Get(id, "FORK_POINTS"); got != "api-a=a-cut,api-b=b-cut" {
		t.Errorf("FORK_POINTS = %q, want the recorded pins kept unchanged", got)
	}
}

// QA brief 4 / AC19: a slice that changes exactly one child behaves like the
// single-repo flow — one ship target, no plural chrome anywhere.
func TestQASingleChangedChildStaysSingular(t *testing.T) {
	id, branch := "COD-99006", "feature/COD-99006-x"
	gits := map[string]*childGit{"api-a": {}, "api-b": {}, "api-c": {}}
	ghs := map[string]*epicGitHub{
		"api-a": {},
		"api-b": {createURL: "https://github.com/acme/api-b/pull/9"},
		"api-c": {},
	}
	p, _ := qaFolder(t, id, branch, gits, ghs)
	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-b"].status = " M b.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}
	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-b" {
		t.Errorf("SHIP_TARGETS = %q, want only api-b", got)
	}
	if got := p.State.Get(id, "PR_URL"); got != "https://github.com/acme/api-b/pull/9" {
		t.Errorf("PR_URL = %q", got)
	}
	if strings.Contains(ghs["api-b"].body, "Ships with") || len(ghs["api-b"].bodyEdits) > 0 {
		t.Errorf("single-child ship carries plural chrome: body=%q edits=%v", ghs["api-b"].body, ghs["api-b"].bodyEdits)
	}
}
