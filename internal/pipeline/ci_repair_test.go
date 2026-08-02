package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/state"
)

var (
	ciRed     = []Check{{Name: "ci/test", Bucket: "fail"}}
	ciGreenOK = []Check{{Name: "ci/test", Bucket: "pass"}}
	ciPending = []Check{{Name: "ci/test", Bucket: "pending"}}
)

// ciRepairGitHub answers the merge gate's CI polls from a scripted sequence of
// check sets, holding the last one once the script runs out, so a test says
// exactly how many polls the repair loop must survive.
type ciRepairGitHub struct {
	mergeGitHub
	rounds [][]Check
	polls  int
}

func (g *ciRepairGitHub) Checks(context.Context, string) ([]Check, error) {
	round := g.rounds[min(g.polls, len(g.rounds)-1)]
	g.polls++
	return round, nil
}

func newCIRepairPipeline(t *testing.T, git Git, gh GitHub, tr *fakeTracker, maxRepairs int) (*Pipeline, *promptLog) {
	t.Helper()
	calls := &promptLog{}
	p := newMergePipeline(t, git, &mergeGitHub{}, tr)
	p.GitHub = gh
	p.Runner = fakeRunner{calls: calls}
	p.PhaseLogs = newMemPhaseLogs()
	p.MaxRepairs = maxRepairs
	return p, calls
}

func agentLabels(calls *promptLog) []string {
	labels := []string{}
	for _, c := range calls.all() {
		labels = append(labels, c.label)
	}
	return labels
}

// The motivating incident (COD-1376): a red check at the merge gate used to
// quarantine the ticket outright. It now drives a ci-repair agent on the slice
// branch, whose pushed fix turns CI green and the PR merges normally.
func TestCIAndMergeRepairsRedCIThenMerges(t *testing.T) {
	id := "COD-91455"
	git := &mergeGit{branch: "feature/COD-91455-ci-gate"}
	gh := &ciRepairGitHub{rounds: [][]Check{ciRed, ciGreenOK}}
	tr := &fakeTracker{}
	rec := &activityRecorder{}
	p, calls := newCIRepairPipeline(t, git, gh, tr, 2)
	p.OnActivity = rec.hook()
	seedPROpen(t, p, id, "312", git.branch)

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if got, want := agentLabels(calls), []string{"ci-repair1"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("agent labels = %v, want %v", got, want)
	}
	// The repair rides on the Merge Activity, not Repair: the merge gate is mid-Ship
	// and Repair maps to the Verify Step, which would walk every stepper backwards.
	wantActivities := []reportedActivity{
		{id, "ci-wait", ""},
		{id, "merge", "ci-repair1/2"},
		{id, "ci-wait", ""},
		{id, "merge", ""},
	}
	if got := fmt.Sprint(rec.seen); got != fmt.Sprint(wantActivities) {
		t.Errorf("reported activities = %v, want %v", rec.seen, wantActivities)
	}
	if _, ok := p.PhaseLogs.(*memPhaseLogs).get(id, "ci-repair1"); !ok {
		t.Error("run record has no ci-repair1 step")
	}
	want := []string{"checkout " + git.branch, "push origin " + git.branch}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
	if gh.polls != 2 {
		t.Errorf("CI polled %d times, want 2 (red, then green after the repair)", gh.polls)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("Merge called %d times, want 1", gh.mergeCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged", got)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
	}
}

// The repair prompt names the PR whose checks are red and the branch the agent is
// standing on.
func TestCIRepairPromptNamesThePRAndBranch(t *testing.T) {
	id := "COD-91456"
	git := &mergeGit{branch: "feature/COD-91456-ci-gate"}
	gh := &ciRepairGitHub{rounds: [][]Check{ciRed, ciGreenOK}}
	p, calls := newCIRepairPipeline(t, git, gh, &fakeTracker{}, 1)
	seedPROpen(t, p, id, "312", git.branch)

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	all := calls.all()
	if len(all) != 1 {
		t.Fatalf("%d agent calls, want 1", len(all))
	}
	for _, want := range []string{"https://x/pr/312", git.branch, id} {
		if !strings.Contains(all[0].prompt, want) {
			t.Errorf("ci-repair prompt does not name %q: %q", want, all[0].prompt)
		}
	}
}

// A stored override replaces the ci_repair body like any other registry prompt.
func TestCIRepairPromptHonorsOverride(t *testing.T) {
	id := "COD-91463"
	git := &mergeGit{branch: "feature/COD-91463-ci-gate"}
	gh := &ciRepairGitHub{rounds: [][]Check{ciRed, ciGreenOK}}
	p, calls := newCIRepairPipeline(t, git, gh, &fakeTracker{}, 1)
	p.prompts = prompts.Renderer{Overrides: map[string]string{"ci_repair": "Custom CI repair for {{.ID}} on {{.Branch}} ({{.PRURL}})."}}
	seedPROpen(t, p, id, "312", git.branch)

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	want := "Custom CI repair for " + id + " on " + git.branch + " (https://x/pr/312)."
	if got := calls.all()[0].prompt; got != want {
		t.Errorf("ci-repair prompt = %q, want %q", got, want)
	}
}

// Exhausted repairs still quarantine — but the reason says how many were spent,
// the branch and PR stay put for the human-driven fallback, and the session keeps
// going (a give-up, never a fault).
func TestCIAndMergeQuarantinesAfterRepairsExhausted(t *testing.T) {
	id := "COD-91457"
	git := &mergeGit{branch: "feature/COD-91457-ci-gate"}
	gh := &ciRepairGitHub{rounds: [][]Check{ciRed}}
	tr := &fakeTracker{}
	p, calls := newCIRepairPipeline(t, git, gh, tr, 3)
	seedPROpen(t, p, id, "312", git.branch)

	err := p.CIAndMerge(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if want := "CI not green after 3 repair attempt(s)"; giveUp.Reason != want {
		t.Errorf("give-up reason = %q, want %q", giveUp.Reason, want)
	}
	if got, want := agentLabels(calls), []string{"ci-repair1", "ci-repair2", "ci-repair3"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("agent labels = %v, want %v", got, want)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0", gh.mergeCalls)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine called %d times, want 1", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want quarantined", got)
	}
	if got := p.State.Get(id, "BRANCH"); got != git.branch {
		t.Errorf("BRANCH = %q, want %q preserved", got, git.branch)
	}
	if got := p.State.Get(id, "PR"); got != "312" {
		t.Errorf("PR = %q, want 312 preserved", got)
	}
}

// An agent that faults mid-repair — an exhausted provider chain, an operator stop —
// is not a CI verdict: the run faults and stays resumable instead of quarantining
// the ticket for attempts it never got to spend.
func TestCIAndMergeFaultsWhenRepairAgentFails(t *testing.T) {
	id := "COD-91464"
	git := &mergeGit{branch: "feature/COD-91464-ci-gate"}
	gh := &ciRepairGitHub{rounds: [][]Check{ciRed}}
	tr := &fakeTracker{}
	p, _ := newCIRepairPipeline(t, git, gh, tr, 3)
	agentErr := errors.New("agent timed out")
	p.Runner = fakeRunner{err: agentErr}
	seedPROpen(t, p, id, "312", git.branch)

	err := p.CIAndMerge(context.Background(), id)

	if !errors.Is(err, agentErr) {
		t.Fatalf("CIAndMerge = %v, want the agent error", err)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.PROpen {
		t.Errorf("PHASE = %q, want %q preserved", got, state.PROpen)
	}
}

// A gate that timed out has no failing check for an agent to read, and MAX_REPAIRS
// more CI_TIMEOUT waits would stall the loop for hours: it quarantines at once,
// exactly as before.
func TestCIAndMergeTimeoutQuarantinesWithoutRepair(t *testing.T) {
	id := "COD-91458"
	git := &mergeGit{branch: "feature/COD-91458-ci-gate"}
	gh := &ciRepairGitHub{rounds: [][]Check{ciPending}}
	tr := &fakeTracker{}
	p, calls := newCIRepairPipeline(t, git, gh, tr, 3)
	seedPROpen(t, p, id, "312", git.branch)

	err := p.CIAndMerge(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if giveUp.Reason != "CI not green" {
		t.Errorf("give-up reason = %q, want %q", giveUp.Reason, "CI not green")
	}
	if n := len(calls.all()); n != 0 {
		t.Errorf("%d agent calls on a CI timeout, want 0", n)
	}
	if gh.polls != 1 {
		t.Errorf("CI polled %d times, want 1", gh.polls)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine called %d times, want 1", tr.quarantineCalls)
	}
}

// The re-gate after an unmergeable PR is synced with its base runs the same repair
// loop, so a red check there retries the merge instead of quarantining.
func TestRecoverUnmergeablePRRepairsRedCIThenMerges(t *testing.T) {
	id := "COD-91459"
	git := &mergeGit{branch: "feature/COD-91459-ci-gate"}
	gh := &ciRepairGitHub{
		mergeGitHub: mergeGitHub{mergeErrs: []error{errNotMergeable}},
		rounds:      [][]Check{ciGreenOK, ciRed, ciGreenOK},
	}
	tr := &fakeTracker{}
	p, calls := newCIRepairPipeline(t, git, gh, tr, 2)
	seedPROpen(t, p, id, "312", git.branch)

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if got, want := agentLabels(calls), []string{"ci-repair1"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("agent labels = %v, want %v", got, want)
	}
	want := []string{
		"checkout " + git.branch,
		"mergeRemote origin main",
		"push origin " + git.branch,
		"checkout " + git.branch,
		"push origin " + git.branch,
	}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
	if gh.mergeCalls != 2 {
		t.Errorf("Merge called %d times, want 2 (refused, then after the repair)", gh.mergeCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged", got)
	}
}

// Exhausted repairs at the post-sync gate keep naming the sync that got them there.
func TestRecoverUnmergeablePRQuarantinesAfterRepairsExhausted(t *testing.T) {
	id := "COD-91460"
	git := &mergeGit{branch: "feature/COD-91460-ci-gate"}
	gh := &ciRepairGitHub{
		mergeGitHub: mergeGitHub{mergeErrs: []error{errNotMergeable}},
		rounds:      [][]Check{ciGreenOK, ciRed},
	}
	p, calls := newCIRepairPipeline(t, git, gh, &fakeTracker{}, 2)
	seedPROpen(t, p, id, "312", git.branch)

	err := p.CIAndMerge(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if want := "CI not green after syncing the PR with main and 2 repair attempt(s)"; giveUp.Reason != want {
		t.Errorf("give-up reason = %q, want %q", giveUp.Reason, want)
	}
	if n := len(calls.all()); n != 2 {
		t.Errorf("%d agent calls, want 2", n)
	}
}

// MAX_REPAIRS=0 turns the loop off entirely and reproduces the pre-repair gate at
// both the merge gate and the post-sync re-gate.
func TestCIGatesWithoutRepairsQuarantineImmediately(t *testing.T) {
	cases := []struct {
		name       string
		rounds     [][]Check
		mergeErrs  []error
		wantReason string
	}{
		{
			name:       "merge gate",
			rounds:     [][]Check{ciRed},
			wantReason: "CI not green",
		},
		{
			name:       "post-sync gate",
			rounds:     [][]Check{ciGreenOK, ciRed},
			mergeErrs:  []error{errNotMergeable},
			wantReason: "CI not green after syncing the PR with main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-91461"
			git := &mergeGit{branch: "feature/COD-91461-ci-gate"}
			gh := &ciRepairGitHub{mergeGitHub: mergeGitHub{mergeErrs: tc.mergeErrs}, rounds: tc.rounds}
			tr := &fakeTracker{}
			p, calls := newCIRepairPipeline(t, git, gh, tr, 0)
			seedPROpen(t, p, id, "312", git.branch)

			err := p.CIAndMerge(context.Background(), id)

			var giveUp *GiveUpError
			if !errors.As(err, &giveUp) {
				t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
			}
			if giveUp.Reason != tc.wantReason {
				t.Errorf("give-up reason = %q, want %q", giveUp.Reason, tc.wantReason)
			}
			if n := len(calls.all()); n != 0 {
				t.Errorf("%d agent calls with MAX_REPAIRS=0, want 0", n)
			}
			if tr.quarantineCalls != 1 {
				t.Errorf("Quarantine called %d times, want 1", tr.quarantineCalls)
			}
		})
	}
}

// A folder repo ships several child repos together, so the first red child still
// gives the whole ticket up untouched — no repair loop, nothing merged.
func TestFolderCIAndMergeStillFailsFastOnRedChild(t *testing.T) {
	id := "COD-91462"
	branch := "feature/COD-91462-ci-gate"
	root := t.TempDir()
	gits := map[string]*childGit{"api-billing": {hasBranch: true}, "api-users": {hasBranch: true}}
	ghs := map[string]*epicGitHub{
		"api-billing": {prState: "OPEN", checks: ciRed},
		"api-users":   {prState: "OPEN", checks: ciGreenOK},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	calls := &promptLog{}
	tr := &fakeTracker{}
	p := newTestPipeline(t, fakeRunner{calls: calls}, tr)
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.AutoMerge = true
	p.MergeMethod = "squash"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"BRANCH":       branch,
		"PHASE":        state.PROpen,
		"SHIP_TARGETS": "api-billing,api-users",
		"PR_URLS":      "api-billing=https://github.com/acme/api-billing/pull/7,api-users=https://github.com/acme/api-users/pull/3",
	} {
		mustSet(t, p, id, key, value)
	}

	err := p.CIAndMerge(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if want := "CI not green in api-billing"; giveUp.Reason != want {
		t.Errorf("give-up reason = %q, want %q", giveUp.Reason, want)
	}
	if n := len(calls.all()); n != 0 {
		t.Errorf("%d agent calls in a folder repo, want 0", n)
	}
	for name, gh := range ghs {
		if gh.mergeCalls != 0 {
			t.Errorf("%s Merge called %d times, want 0", name, gh.mergeCalls)
		}
	}
}
