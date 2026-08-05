package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// The stacked epic shape (EPIC_STACKED_PRS). Every test here is fake-backed: the
// question is always what trau asks git and gh for, never what GitHub does with it.

const stackedEpicID = "COD-8100"

// greenChecks is a CI rollup every gate waves through.
var greenChecks = []Check{{Name: "ci/test", Bucket: "pass"}}

// stackedGit is a localGit that remembers the branches it created, so a chain built
// one layer at a time can answer "does the layer below exist?" the way a real repo
// would, and so a test can read back what each branch was cut from. Its remote
// carries every branch it created, plus whatever remoteBranches names — the second
// machine's view of a chain this checkout has never seen.
type stackedGit struct {
	localGit
	created          map[string]string // branch -> the ref it was cut from
	epicBranch       string
	remoteEpic       string
	remoteBranches   map[string]bool
	checkedOutRemote []string
	deleted          []string
}

func newStackedGit() *stackedGit {
	return &stackedGit{
		localGit:       localGit{hasRemote: true},
		created:        map[string]string{},
		remoteBranches: map[string]bool{},
	}
}

func (g *stackedGit) CreateBranch(_ context.Context, branch, base string) error {
	g.created[branch] = base
	g.branch = branch
	return nil
}

func (g *stackedGit) BranchExists(_ context.Context, branch string) (bool, error) {
	_, ok := g.created[branch]
	return ok, nil
}

func (g *stackedGit) FindEpicBranch(context.Context, string) (string, error) {
	return g.epicBranch, nil
}

func (g *stackedGit) FindRemoteEpicBranch(context.Context, string, string) (string, error) {
	return g.remoteEpic, nil
}

func (g *stackedGit) RemoteBranchExists(_ context.Context, _, branch string) (bool, error) {
	_, local := g.created[branch]
	return local || g.remoteBranches[branch], nil
}

func (g *stackedGit) CheckoutRemoteBranch(_ context.Context, _, branch string) error {
	g.checkedOutRemote = append(g.checkedOutRemote, branch)
	g.created[branch] = "remote"
	return nil
}

func (g *stackedGit) HeadSHA(context.Context) (string, error) {
	return "sha-" + g.branch, nil
}

// RemoteSHA reports a tip for the base and for every branch the remote carries, so
// the PR-base gate sees a remote that is current rather than one missing the branch.
func (g *stackedGit) RemoteSHA(_ context.Context, _, branch string) (string, error) {
	if branch == prBaseBranch {
		return prBaseRemoteTip, nil
	}
	if _, ok := g.created[branch]; ok || g.remoteBranches[branch] {
		return "sha-" + branch, nil
	}
	return "", nil
}

func (g *stackedGit) DeleteBranch(_ context.Context, branch string) error {
	g.deleted = append(g.deleted, branch)
	return nil
}

// stackedGitHub scripts the stack seam: which branch has which open PR, what state
// each PR reads, and what the PR bases came out as. flipMergedAfter models the
// operator merging the stack by hand during the AUTO_MERGE=0 wait.
type stackedGitHub struct {
	epicGitHub
	openPRs         map[string]string // branch -> open PR URL
	prBases         []string
	stateCalls      int
	flipMergedAfter int
}

func (g *stackedGitHub) openPR(branch, url string) {
	if g.openPRs == nil {
		g.openPRs = map[string]string{}
	}
	g.openPRs[branch] = url
}

func (g *stackedGitHub) PRURL(_ context.Context, branch string) (string, error) {
	return g.openPRs[branch], nil
}

func (g *stackedGitHub) PRState(_ context.Context, pr string) (string, error) {
	g.stateCalls++
	if g.flipMergedAfter > 0 && g.stateCalls > g.flipMergedAfter {
		return "MERGED", nil
	}
	return "OPEN", nil
}

func (g *stackedGitHub) CreatePR(ctx context.Context, base, head, title, body string, draft bool) (string, error) {
	g.prBases = append(g.prBases, base)
	return g.epicGitHub.CreatePR(ctx, base, head, title, body, draft)
}

func newStackedPipeline(t *testing.T, git Git, gh GitHub, tr tracker.Tracker) *Pipeline {
	t.Helper()
	p := localTestPipeline(t, git, gh, tr)
	p.EpicID = stackedEpicID
	p.EpicStackedPRs = true
	p.Sleep = func(time.Duration) {}
	p.CIPoll = 1
	p.RequireCI = config.CIGateOn
	return p
}

// The mode engages only when every gate passes, and the verdict is reached once:
// the flag alone is not enough, and a repo that cannot stack runs the classic epic
// flow for the whole epic rather than for some of its children.
func TestStackedEpicGatingMatrix(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(p *Pipeline, git *stackedGit, gh *stackedGitHub)
		want    bool
		wantLog string
	}{
		{
			name:  "every gate passes",
			setup: func(_ *Pipeline, _ *stackedGit, gh *stackedGitHub) { gh.stacksEnabled = true },
			want:  true,
		},
		{
			name: "flag off",
			setup: func(p *Pipeline, _ *stackedGit, gh *stackedGitHub) {
				p.EpicStackedPRs, gh.stacksEnabled = false, true
			},
		},
		{
			name: "not a GitHub remote",
			setup: func(p *Pipeline, _ *stackedGit, gh *stackedGitHub) {
				p.Forge, gh.stacksEnabled = "gitlab", true
			},
			wantLog: "GitLab",
		},
		{
			name: "local delivery",
			setup: func(_ *Pipeline, git *stackedGit, gh *stackedGitHub) {
				git.hasRemote, gh.stacksEnabled = false, true
			},
			wantLog: "no remote",
		},
		{
			name: "an epic branch already owns the epic",
			setup: func(_ *Pipeline, git *stackedGit, gh *stackedGitHub) {
				git.epicBranch, gh.stacksEnabled = "epic/"+stackedEpicID+"-legacy", true
			},
			wantLog: "epic branch",
		},
		{
			name: "another machine already cut the epic branch",
			setup: func(_ *Pipeline, git *stackedGit, gh *stackedGitHub) {
				git.remoteEpic, gh.stacksEnabled = "epic/"+stackedEpicID+"-legacy", true
			},
			wantLog: "origin already carries",
		},
		{
			name:    "the preview is not enabled",
			setup:   func(_ *Pipeline, _ *stackedGit, gh *stackedGitHub) { gh.stacksEnabled = false },
			wantLog: "not enabled",
		},
		{
			name: "the probe errors",
			setup: func(_ *Pipeline, _ *stackedGit, gh *stackedGitHub) {
				gh.stacksErr = errors.New("gh api stacks: HTTP 404")
			},
			wantLog: "did not answer",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			git, gh, log := newStackedGit(), &stackedGitHub{}, &logRenderer{}
			p := newStackedPipeline(t, git, gh, &epicTracker{})
			p.Renderer = log
			c.setup(p, git, gh)

			if got := p.stackedEpic(context.Background()); got != c.want {
				t.Fatalf("stackedEpic = %v, want %v", got, c.want)
			}
			if c.wantLog != "" && !log.contains(c.wantLog) {
				t.Errorf("log = %v, want one line naming %q", log.lines, c.wantLog)
			}
			// The shape is settled at epic start: an answer that changes underneath
			// must not flip the epic mid-flight.
			gh.stacksEnabled, gh.stacksErr = !c.want, nil
			if got := p.stackedEpic(context.Background()); got != c.want {
				t.Errorf("stackedEpic re-read = %v, want the memoized %v", got, c.want)
			}
			if gh.stacksProbes > 1 {
				t.Errorf("probed the stacks endpoint %d times, want at most one per epic", gh.stacksProbes)
			}
		})
	}
}

// A stacked epic's slices chain: the first is cut from the base as the remote has
// it and its PR targets the base; every one after is cut from the branch below it
// and its PR targets that same branch. No epic branch is ever created, and the
// stack is linked, bottom-first, each time it grows.
func TestStackedEpicChainsBranchAndPRBasesAcrossChildren(t *testing.T) {
	git, gh := newStackedGit(), &stackedGitHub{}
	gh.stacksEnabled = true
	tr := &epicTracker{title: "Layer"}
	p := newStackedPipeline(t, git, gh, tr)

	children := []string{"COD-8101", "COD-8102", "COD-8103"}
	for _, id := range children {
		tr.subs = append(tr.subs, tracker.SubIssue{ID: id, Title: "Layer"})
	}

	var branches []string
	for i, id := range children {
		gh.createURL = fmt.Sprintf("https://github.test/pr/%d", 900+i)
		branches = append(branches, runStackedChild(t, p, gh, id))
	}

	wantCutFrom := []string{"origin/main", branches[0], branches[1]}
	for i, branch := range branches {
		if got := git.created[branch]; got != wantCutFrom[i] {
			t.Errorf("%s was cut from %q, want %q", branch, got, wantCutFrom[i])
		}
	}
	wantPRBase := []string{"main", branches[0], branches[1]}
	if len(gh.prBases) != len(wantPRBase) {
		t.Fatalf("opened %d PR(s) (bases %v), want %d", len(gh.prBases), gh.prBases, len(wantPRBase))
	}
	for i, want := range wantPRBase {
		if gh.prBases[i] != want {
			t.Errorf("PR %d targets %q, want %q", i+1, gh.prBases[i], want)
		}
	}
	// The stack is linked bottom-first, and only once there are two layers to link.
	wantLinks := [][]string{branches[:2], branches[:3]}
	if len(gh.linked) != len(wantLinks) {
		t.Fatalf("linked the stack %d time(s) (%v), want %d", len(gh.linked), gh.linked, len(wantLinks))
	}
	for i, want := range wantLinks {
		if strings.Join(gh.linked[i], ",") != strings.Join(want, ",") {
			t.Errorf("link %d = %v, want %v", i+1, gh.linked[i], want)
		}
	}
	for branch, base := range git.created {
		if strings.HasPrefix(branch, "epic/") || strings.HasPrefix(base, "epic/") {
			t.Errorf("branch %s ← %s: a stacked epic must create no epic branch", branch, base)
		}
	}
	if len(git.deleted) != 0 {
		t.Errorf("deleted %v, want nothing deleted while the stack is in flight", git.deleted)
	}
}

// runStackedChild drives one child through the two phases that decide the stack's
// shape — cutting its branch and opening its PR — and returns the branch it used.
func runStackedChild(t *testing.T, p *Pipeline, gh *stackedGitHub, id string) string {
	t.Helper()
	ctx := context.Background()
	p.primeStackLayer(id)
	branch, err := p.resolveBuildBranch(ctx, id)
	if err != nil {
		t.Fatalf("resolveBuildBranch(%s) = %v", id, err)
	}
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}
	p.recordDiffBase(ctx, id)
	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR(%s) = %v", id, err)
	}
	gh.openPR(branch, p.State.Get(id, "PR_URL"))
	return branch
}

// A child of a stacked epic is complete when its PR is open, linked and green — it
// does NOT merge. Merging a layer early is what makes GitHub rewrite the layers
// above it, which is the whole reason this shape merges once, at the end.
func TestStackedEpicChildCompletesWithoutMerging(t *testing.T) {
	id := "COD-8110"
	branch := "feature/COD-8110-layer"
	git, gh := newStackedGit(), &stackedGitHub{}
	gh.stacksEnabled, gh.checks = true, greenChecks
	git.created[branch] = "main"
	tr := &epicTracker{subs: []tracker.SubIssue{{ID: id}}}
	p := newStackedPipeline(t, git, gh, tr)
	seedPROpen(t, p, id, "941", branch)
	if err := p.State.Set(id, "BASE", "main"); err != nil {
		t.Fatal(err)
	}

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if gh.mergeCalls != 0 || gh.stackMergeCalls != 0 {
		t.Errorf("merged the layer (%d legacy, %d stack), want neither until the epic finalizes", gh.mergeCalls, gh.stackMergeCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want %q so the epic gate counts the layer delivered", got, state.Merged)
	}
	if got := p.State.Get(id, "PR_STATUS"); got != prStatusAwaitingMerge {
		t.Errorf("PR_STATUS = %q, want %q — the layer's PR is still waiting for the stack", got, prStatusAwaitingMerge)
	}
	if tr.setStatus != tracker.StageDone {
		t.Errorf("tracker status = %v, want Done", tr.setStatus)
	}
}

// seedStack lays down a finished chain of layers, as the children left it.
func seedStack(t *testing.T, p *Pipeline, tr *epicTracker, layers int) []stackLayer {
	t.Helper()
	base := p.Base
	var chain []stackLayer
	if tr.status == nil {
		tr.status = map[string]tracker.IssueStatus{}
	}
	for i := 1; i <= layers; i++ {
		id := fmt.Sprintf("COD-81%02d", i)
		layer := stackLayer{
			ID:     id,
			Branch: fmt.Sprintf("feature/%s-layer%d", id, i),
			Base:   base,
			PR:     fmt.Sprintf("%d", 950+i),
			PRURL:  fmt.Sprintf("https://github.test/pr/%d", 950+i),
		}
		tr.subs = append(tr.subs, tracker.SubIssue{ID: id})
		tr.status[id] = tracker.StatusDone
		for k, v := range map[string]string{
			"BRANCH": layer.Branch, "BASE": layer.Base, "PR": layer.PR,
			"PR_URL": layer.PRURL, "PHASE": state.Merged, "TRACKER_DONE": "1",
		} {
			if err := p.State.Set(id, k, v); err != nil {
				t.Fatal(err)
			}
		}
		chain = append(chain, layer)
		base = layer.Branch
	}
	return chain
}

// The green path: every layer is polled, the whole stack merges once from the top
// PR, and no epic PR is ever opened or merged the legacy way.
func TestFinalizeStackedEpicMergesTopPR(t *testing.T) {
	git, gh := newStackedGit(), &stackedGitHub{}
	gh.stacksEnabled, gh.checks = true, greenChecks
	tr := &epicTracker{title: "Stacked epic"}
	p := newStackedPipeline(t, git, gh, tr)
	chain := seedStack(t, p, tr, 3)

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic = %v, want nil", err)
	}
	top := chain[len(chain)-1]
	if gh.stackMergeCalls != 1 || gh.stackMergedPR != top.PR {
		t.Fatalf("stack merged %d time(s) from PR %q, want once from the top PR %q", gh.stackMergeCalls, gh.stackMergedPR, top.PR)
	}
	if gh.stackMergeMethod != "squash" {
		t.Errorf("stack merge method = %q, want the configured squash", gh.stackMergeMethod)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("the legacy merge endpoint was called %d time(s) — it refuses every stacked PR", gh.mergeCalls)
	}
	if gh.createCalls != 0 {
		t.Errorf("opened %d epic PR(s), want none — a stacked epic has no epic PR", gh.createCalls)
	}
	want := []string{chain[0].Branch, chain[1].Branch, chain[2].Branch}
	if len(gh.linked) != 1 || strings.Join(gh.linked[0], ",") != strings.Join(want, ",") {
		t.Errorf("re-linked %v before merging, want the whole chain bottom-first", gh.linked)
	}
	if got := p.State.Get(stackedEpicID, "PHASE"); got != state.Merged {
		t.Errorf("epic PHASE = %q, want %q", got, state.Merged)
	}
	if set := tr.setFor(stackedEpicID); set == nil || set.stage != tracker.StageDone {
		t.Errorf("epic status = %+v, want Done", set)
	}
	if len(git.created) != 0 {
		t.Errorf("created %v while finalizing, want no branch — a stacked epic has no epic branch", git.created)
	}
}

// A layer that is not green stops the finalize: nothing merges, and the whole stack
// is left standing for a human. Merging around a bad layer is exactly the partial
// merge this shape exists to avoid.
func TestFinalizeStackedEpicHandsOffOnARedLayer(t *testing.T) {
	gh := &stackedGitHub{}
	gh.stacksEnabled, gh.checks = true, []Check{{Name: "ci/test", Bucket: "fail"}}
	tr := &epicTracker{title: "Stacked epic"}
	p := newStackedPipeline(t, newStackedGit(), gh, tr)
	seedStack(t, p, tr, 2)

	err := p.FinalizeEpic(context.Background())
	var handoff *EpicHandOffError
	if !errors.As(err, &handoff) {
		t.Fatalf("FinalizeEpic = %v, want an *EpicHandOffError", err)
	}
	if !strings.Contains(handoff.Reason, "not green") {
		t.Errorf("hand-off reason = %q, want it to name the red layer", handoff.Reason)
	}
	if gh.stackMergeCalls != 0 {
		t.Errorf("stack merged %d time(s) with a red layer, want 0", gh.stackMergeCalls)
	}
	if got := p.State.Get(stackedEpicID, "RELEASE"); got != state.ReleaseAwaitingHuman {
		t.Errorf("RELEASE = %q, want %q", got, state.ReleaseAwaitingHuman)
	}
}

// gh's documented exit codes each become a give-up a reader can act on, rather than
// one opaque "stack merge failed".
func TestFinalizeStackedEpicMapsStackMergeExitCodes(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{3, "could not rebase the stack"},
		{4, "API rejected it"},
		{9, "no longer available"},
		{1, "boom"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("exit %d", c.code), func(t *testing.T) {
			gh := &stackedGitHub{}
			gh.stacksEnabled, gh.checks = true, greenChecks
			gh.stackMergeErr = &StackMergeError{
				Code:   c.code,
				Output: "boom",
				err:    fmt.Errorf("exit status %d", c.code),
			}
			tr := &epicTracker{title: "Stacked epic"}
			p := newStackedPipeline(t, newStackedGit(), gh, tr)
			seedStack(t, p, tr, 2)

			err := p.FinalizeEpic(context.Background())
			var give *GiveUpError
			if !errors.As(err, &give) {
				t.Fatalf("FinalizeEpic = %v, want a *GiveUpError", err)
			}
			if !strings.Contains(give.Reason, c.want) {
				t.Errorf("give-up reason = %q, want it to name %q", give.Reason, c.want)
			}
			if gh.stackMergeCalls != 1 {
				t.Errorf("attempted the stack merge %d time(s), want exactly one — it is all-or-nothing", gh.stackMergeCalls)
			}
			if got := p.State.Get(stackedEpicID, "RELEASE"); got != state.ReleaseAwaitingHuman {
				t.Errorf("RELEASE = %q, want the release parked for a human", got)
			}
		})
	}
}

// AUTO_MERGE=0 leaves the stack standing and waits: trau merges nothing, and the
// epic closes only once every layer reads merged.
func TestFinalizeStackedEpicWaitsForTheOperatorsMerge(t *testing.T) {
	gh := &stackedGitHub{flipMergedAfter: 3}
	gh.stacksEnabled, gh.checks = true, greenChecks
	tr := &epicTracker{title: "Stacked epic"}
	p := newStackedPipeline(t, newStackedGit(), gh, tr)
	p.AutoMerge = false
	chain := seedStack(t, p, tr, 2)
	top := chain[len(chain)-1]

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic = %v, want nil once the operator merged the stack", err)
	}
	if gh.stackMergeCalls != 0 || gh.mergeCalls != 0 {
		t.Errorf("trau merged something under AUTO_MERGE=0 (%d stack, %d legacy), want 0", gh.stackMergeCalls, gh.mergeCalls)
	}
	if got := p.State.Get(stackedEpicID, "PHASE"); got != state.Merged {
		t.Errorf("epic PHASE = %q, want %q after every layer merged", got, state.Merged)
	}
	if got := p.State.Get(stackedEpicID, "PR"); got != top.PR {
		t.Errorf("epic PR = %q, want the stack's top PR %q", got, top.PR)
	}
	if set := tr.setFor(stackedEpicID); set == nil || set.stage != tracker.StageDone {
		t.Errorf("epic status = %+v, want Done", set)
	}
}

// A later run adopts the partial chain an earlier one left: it rediscovers the
// layers, confirms their PRs against the forge, and stacks the next child on the
// chain's tip instead of starting a second chain from the base.
func TestStackedEpicResumeAdoptsAPartialChain(t *testing.T) {
	git, gh, log := newStackedGit(), &stackedGitHub{}, &logRenderer{}
	gh.stacksEnabled = true
	tr := &epicTracker{title: "Next layer"}
	p := newStackedPipeline(t, git, gh, tr)
	p.Renderer = log
	chain := seedStack(t, p, tr, 2)
	tip := chain[len(chain)-1]
	// The tip's branch exists only on the remote — the second machine's view — and
	// its PR was opened but never recorded, so it has to come back from the forge.
	if err := p.State.Set(tip.ID, "PR", ""); err != nil {
		t.Fatal(err)
	}
	git.remoteBranches[tip.Branch] = true
	gh.openPR(tip.Branch, tip.PRURL)

	id := "COD-8199"
	tr.subs = append(tr.subs, tracker.SubIssue{ID: id})
	p.primeStackLayer(id)
	cut, err := p.stackedCutRef(context.Background(), id)
	if err != nil {
		t.Fatalf("stackedCutRef = %v, want the adopted tip", err)
	}
	if cut != tip.Branch {
		t.Errorf("cut ref = %q, want the chain's tip %q", cut, tip.Branch)
	}
	if got := p.stackLayer.base; got != tip.Branch {
		t.Errorf("recorded layer base = %q, want %q", got, tip.Branch)
	}
	for _, want := range []string{"2 layer(s) adopted", chain[0].Branch, tip.Branch, "PR #" + tip.PR} {
		if !log.contains(want) {
			t.Errorf("run log = %v, want it to record %q as adopted", log.lines, want)
		}
	}
	if len(git.checkedOutRemote) != 1 || git.checkedOutRemote[0] != tip.Branch {
		t.Errorf("adopted %v from the remote, want only the tip %q", git.checkedOutRemote, tip.Branch)
	}
}

// The invariants this shape rests on, read straight off its source: it rebases
// nothing, force-pushes nothing and retargets no PR. Every git and gh call it makes
// goes through the Git/GitHub seams, and neither offers those operations at all.
func TestStackedEpicSourceRewritesNoHistory(t *testing.T) {
	src, err := os.ReadFile("stacked_epic.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"git rebase", "stack rebase", "--force", "force-with-lease", "pr edit", "--base"} {
		if strings.Contains(string(src), forbidden) {
			t.Errorf("stacked_epic.go contains %q — the stacked shape must never rewrite history or retarget a PR", forbidden)
		}
	}
}
