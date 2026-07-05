package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/state"
)

// errNotMergeable mirrors gh's real refusal, stderr included (see withStderr) —
// the exact failure COD-702 hit when a sibling squash-merged into the epic
// branch after its PR opened.
var errNotMergeable = errors.New("gh pr merge: exit status 1: X Pull request RomkaLTU/trau#105 is not mergeable: the merge commit cannot be cleanly created")

func TestUnmergeablePR(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"conflicting", errNotMergeable, true},
		{"policy block", errors.New("X Pull request #7 is not mergeable: the base branch policy prohibits the merge"), true},
		{"merge conflict", errors.New("merge conflict between branches"), true},
		{"transient", errors.New("dial tcp: i/o timeout"), false},
		{"auth", errors.New("HTTP 403: Forbidden"), false},
	}
	for _, c := range cases {
		if got := unmergeablePR(c.err); got != c.want {
			t.Errorf("%s: unmergeablePR=%v want %v", c.name, got, c.want)
		}
	}
}

// mergeGit records the git traffic of the conflict-recovery path in order, so
// tests can assert both what happened and the sequence it happened in.
type mergeGit struct {
	fakeGit
	ops          []string
	conflicted   bool // first MergeRemote reports conflicts
	unmergedLeft int  // Unmerged() calls that still report conflicted files
	branch       string
}

func (g *mergeGit) op(format string, a ...any) { g.ops = append(g.ops, fmt.Sprintf(format, a...)) }

func (g *mergeGit) Pull(_ context.Context, remote, branch string) error {
	g.op("pull %s %s", remote, branch)
	return nil
}
func (g *mergeGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.op("checkout %s", ref)
	return nil
}
func (g *mergeGit) BranchExists(_ context.Context, branch string) (bool, error) {
	return branch == g.branch, nil
}
func (g *mergeGit) MergeRemote(_ context.Context, remote, base string) (bool, error) {
	g.op("mergeRemote %s %s", remote, base)
	c := g.conflicted
	g.conflicted = false
	return c, nil
}
func (g *mergeGit) Unmerged(context.Context) (string, error) {
	if g.unmergedLeft > 0 {
		g.unmergedLeft--
		return "both modified: internal/x.go", nil
	}
	return "", nil
}
func (g *mergeGit) ContinueMerge(context.Context) error {
	g.op("continueMerge")
	return nil
}
func (g *mergeGit) MergeAbort(context.Context) error {
	g.op("mergeAbort")
	return nil
}
func (g *mergeGit) Push(_ context.Context, remote, ref string, _ bool) error {
	g.op("push %s %s", remote, ref)
	return nil
}

// mergeGitHub fails Merge with the queued errors first, then with always (nil
// once both are exhausted).
type mergeGitHub struct {
	epicGitHub
	mergeErrs []error
	always    error
}

func (f *mergeGitHub) Merge(ctx context.Context, pr, method string, deleteBranch bool) error {
	i := f.mergeCalls
	_ = f.epicGitHub.Merge(ctx, pr, method, deleteBranch)
	if i < len(f.mergeErrs) {
		return f.mergeErrs[i]
	}
	return f.always
}

func newMergePipeline(t *testing.T, git *mergeGit, gh *mergeGitHub, tr *fakeTracker) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	return &Pipeline{
		Runner:      fakeRunner{},
		Tracker:     tr,
		Git:         git,
		GitHub:      gh,
		State:       state.NewStore(dir),
		RunsDir:     dir,
		Base:        "main",
		Remote:      "origin",
		Prefix:      "COD",
		AutoMerge:   true,
		MergeMethod: "squash",
		MaxRepairs:  1,
		Sleep:       func(time.Duration) {},
	}
}

func seedPROpen(t *testing.T, p *Pipeline, id, pr, branch string) {
	t.Helper()
	for k, v := range map[string]string{"PR": pr, "PR_URL": "https://x/pr/" + pr, "BRANCH": branch, "PHASE": state.PROpen} {
		if err := p.State.Set(id, k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// A PR whose base moved cleanly under it (no textual conflict, e.g. GitHub's
// mergeability was stale) must be synced with the base and merged — never
// surfaced as an "unexpected error" fault. This is the COD-702 regression guard.
func TestCIAndMergeUnmergeableSyncsBaseAndMerges(t *testing.T) {
	id := "COD-90702"
	git := &mergeGit{branch: "feature/COD-90702-x"}
	gh := &mergeGitHub{mergeErrs: []error{errNotMergeable}}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	seedPROpen(t, p, id, "105", git.branch)

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	want := []string{"checkout feature/COD-90702-x", "mergeRemote origin main", "push origin feature/COD-90702-x"}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
	if gh.mergeCalls != 2 {
		t.Errorf("Merge called %d times, want 2 (refused, then after sync)", gh.mergeCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged", got)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
	}
}

// A real conflict (a sibling squash-merged into the epic branch after this PR
// opened) is resolved by the bounded agent loop: merge completed, pushed, PR
// merged into the EPIC branch — the child's base, not main.
func TestCIAndMergeEpicChildConflictResolvedByAgent(t *testing.T) {
	id := "COD-90702"
	git := &mergeGit{branch: "feature/COD-90702-x", conflicted: true}
	gh := &mergeGitHub{mergeErrs: []error{errNotMergeable}}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	p.EpicID = "COD-90697"
	p.epicBranch = "epic/COD-90697-y"
	seedPROpen(t, p, id, "105", git.branch)

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	want := []string{"checkout feature/COD-90702-x", "mergeRemote origin epic/COD-90697-y", "continueMerge", "push origin feature/COD-90702-x"}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged", got)
	}
}

// Conflicts the agents cannot clear within MaxRepairs abort the merge and give
// up (quarantine + needs-human) — the session must keep going, so the error is
// a *GiveUpError, never an opaque fault.
func TestCIAndMergeUnresolvedConflictGivesUp(t *testing.T) {
	id := "COD-90702"
	git := &mergeGit{branch: "feature/COD-90702-x", conflicted: true, unmergedLeft: 99}
	gh := &mergeGitHub{mergeErrs: []error{errNotMergeable}}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	seedPROpen(t, p, id, "105", git.branch)

	err := p.CIAndMerge(context.Background(), id)
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine called %d times, want 1", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want quarantined", got)
	}
	if !strings.Contains(strings.Join(git.ops, "; "), "mergeAbort") {
		t.Errorf("expected the failed merge to be aborted, ops = %q", git.ops)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("Merge called %d times, want 1 (no blind re-merge of an unresolved conflict)", gh.mergeCalls)
	}
}

// A PR GitHub keeps refusing even after a clean sync (e.g. a branch-protection
// policy block) exhausts the paced recompute retries and gives up with the
// reason in hand — not an "unexpected error" fault that stops the session.
func TestCIAndMergePersistentlyUnmergeableGivesUp(t *testing.T) {
	id := "COD-90702"
	git := &mergeGit{branch: "feature/COD-90702-x"}
	gh := &mergeGitHub{always: errors.New("X Pull request #105 is not mergeable: the base branch policy prohibits the merge")}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	seedPROpen(t, p, id, "105", git.branch)

	err := p.CIAndMerge(context.Background(), id)
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if !strings.Contains(g.Reason, "still not mergeable") {
		t.Errorf("give-up reason = %q, want it to name the merge refusal", g.Reason)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine called %d times, want 1", tr.quarantineCalls)
	}
	if gh.mergeCalls != 4 {
		t.Errorf("Merge called %d times, want 4 (initial + 3 paced post-sync attempts)", gh.mergeCalls)
	}
}

// Non-conflict merge failures keep their old path: transient errors exhaust
// retryGH and bubble to the caller (loop-level fault), with no conflict
// recovery and no quarantine.
func TestCIAndMergeTransientFailureStillBubbles(t *testing.T) {
	id := "COD-90702"
	git := &mergeGit{branch: "feature/COD-90702-x"}
	gh := &mergeGitHub{always: errors.New("dial tcp: i/o timeout")}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	seedPROpen(t, p, id, "105", git.branch)

	err := p.CIAndMerge(context.Background(), id)
	if err == nil || isGiveUp(err) {
		t.Fatalf("CIAndMerge = %v, want a plain error (fault path)", err)
	}
	if len(git.ops) != 0 {
		t.Errorf("no conflict recovery expected for a transient failure, git ops = %q", git.ops)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
	}
}

// Before a child branches off, the local epic branch must be fast-forwarded from
// the REMOTE epic: siblings squash-merge into the remote, and a stale local epic
// hands the child a base missing that work — the root cause of the COD-702
// poisoned-branch conflict.
func TestSyncEpicBestPullsRemoteEpicFirst(t *testing.T) {
	git := &mergeGit{}
	p := &Pipeline{Git: git, Remote: "origin", Base: "main"}

	p.syncEpicBest(context.Background(), "epic/COD-90697-y")

	want := []string{"checkout epic/COD-90697-y", "pull origin epic/COD-90697-y", "mergeRemote origin main", "push origin epic/COD-90697-y"}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
}

// The finalize-time sync needs the same freshness: children squash-merged into
// the remote epic while the local copy sat still, and pushing that stale local
// epic would be rejected as non-fast-forward.
func TestSyncEpicForMergePullsRemoteEpicFirst(t *testing.T) {
	git := &mergeGit{}
	p := &Pipeline{Git: git, Remote: "origin", Base: "main", EpicID: "COD-90697"}

	synced, err := p.syncEpicForMerge(context.Background(), "epic/COD-90697-y")
	if err != nil || !synced {
		t.Fatalf("syncEpicForMerge = %v, %v; want true, nil", synced, err)
	}
	want := []string{"checkout epic/COD-90697-y", "pull origin epic/COD-90697-y", "mergeRemote origin main", "push origin epic/COD-90697-y"}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
}
