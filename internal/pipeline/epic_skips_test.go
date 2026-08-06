package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// An epic run declares its skip set once, for the whole epic: every sub-issue run
// and the release that follows them. These tests measure that against
// the fakes — what trau asks the agent, git and gh for — never what a forge does
// with it.

// epicChildren are epic COD-1's sub-issues — the same pair doneEpicTracker reports
// terminal — in the order the loop would reach them.
var epicChildren = []string{"COD-2", "COD-3"}

// countingChecksWaitGitHub counts the CI polls a release spends, so a test can
// tell a gate that was armed and passed from one that never ran at all. The
// operator merges its PR partway through the wait, so a release trau may not merge
// still terminates.
type countingChecksWaitGitHub struct {
	waitGitHub
	checksCalls int
}

func (g *countingChecksWaitGitHub) Checks(ctx context.Context, pr string) ([]Check, error) {
	g.checksCalls++
	return g.waitGitHub.Checks(ctx, pr)
}

// countingChecksStackedGitHub is the same counter on the stacked seam, where the
// gate is polled once per layer rather than once for an epic PR.
type countingChecksStackedGitHub struct {
	stackedGitHub
	checksCalls int
}

func (g *countingChecksStackedGitHub) Checks(ctx context.Context, pr string) ([]Check, error) {
	g.checksCalls++
	return g.stackedGitHub.Checks(ctx, pr)
}

// epicRunPipeline is the loop an operator started on an epic: the same Pipeline
// drives every sub-issue and then the release, so a set declared once has to reach
// all of them.
func epicRunPipeline(t *testing.T, runner *countingRunner, skips []string) *Pipeline {
	t.Helper()
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.EpicID = "COD-1"
	p.EpicRun = true
	p.Skips = skips
	return p
}

// runChildQA walks one sub-issue through the handoff brief and the Verify Step the
// way runPhases does. A provider pause is how a QA agent that actually spawned
// announces itself against these fakes, so it is an outcome the caller reads rather
// than a failure.
func runChildQA(t *testing.T, p *Pipeline, id string) error {
	t.Helper()
	writeHandoff(t, id)
	p.loadSkips(id)
	if err := p.handoffAndCleanup(context.Background(), id, true); err != nil {
		return err
	}
	return p.Verify(context.Background(), id)
}

// The whole epic runs in one process, so the declared set has to reach every
// sub-issue the loop picks — not only the first — and leave each child's own
// checkpoint able to answer for it after a restart.
func TestEpicRunSkipsReachEverySubIssue(t *testing.T) {
	cases := []struct {
		name           string
		skips          []string
		wantAgentCalls int
	}{
		{name: "verify skipped for the epic", skips: []string{config.SkipVerify}},
		{name: "verify and ship skipped", skips: []string{config.SkipVerify, config.SkipCI, config.SkipMerge}},
		{name: "nothing skipped", wantAgentCalls: len(epicChildren)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &countingRunner{results: []error{errors.New(rateLimited)}, name: "claude"}
			p := epicRunPipeline(t, runner, tc.skips)
			for _, id := range epicChildren {
				switch err := runChildQA(t, p, id); {
				case len(tc.skips) > 0 && err != nil:
					t.Fatalf("%s: %v, want nil — the epic run skipped the Verify Step", id, err)
				case len(tc.skips) == 0 && !IsPaused(err):
					t.Fatalf("%s: %v, want the pause a spawned QA agent surfaces", id, err)
				}
			}
			if runner.calls != tc.wantAgentCalls {
				t.Errorf("agent calls across %d sub-issue(s) = %d, want %d", len(epicChildren), runner.calls, tc.wantAgentCalls)
			}
			if len(tc.skips) == 0 {
				return
			}
			for _, id := range epicChildren {
				if got := p.State.Get(id, "PHASE"); got != state.Verified {
					t.Errorf("%s PHASE = %q, want %q — a skipped verify still advances the checkpoint", id, got, state.Verified)
				}
				if got := p.State.Get(id, state.SkipsKey); got != state.EncodeSkips(tc.skips) {
					t.Errorf("%s recorded SKIPS = %q, want %q", id, got, state.EncodeSkips(tc.skips))
				}
			}
			if got := p.State.Get(p.EpicID, state.SkipsKey); got != state.EncodeSkips(tc.skips) {
				t.Errorf("epic recorded SKIPS = %q, want %q — the release resumes off it", got, state.EncodeSkips(tc.skips))
			}
		})
	}
}

// Only a run that drives the whole epic may speak for the epic. A run that skips
// nothing leaves the epic's checkpoint untouched — the release opens that row, and
// a row with no phase reads as a run stuck in flight — and a single sub-issue run
// that merely builds on the epic branch keeps its set to itself rather than
// lowering the bar for every sibling a later run picks up.
func TestOnlyAnEpicRunRecordsTheEpicsSkips(t *testing.T) {
	cases := []struct {
		name    string
		epicRun bool
		skips   []string
		want    string
	}{
		{name: "epic run records its declared set", epicRun: true, skips: []string{config.SkipVerify}, want: "verify"},
		{name: "epic run with no skips writes nothing", epicRun: true},
		{name: "single sub-issue run keeps its set to itself", skips: []string{config.SkipVerify}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.EpicID, p.EpicRun, p.Skips = "COD-1", tc.epicRun, tc.skips

			p.loadSkips("COD-2")

			if got := p.State.Get(p.EpicID, state.SkipsKey); got != tc.want {
				t.Errorf("epic SKIPS = %q, want %q", got, tc.want)
			}
			if got := p.State.Get(p.EpicID, "PHASE"); got != "" {
				t.Errorf("epic PHASE = %q, want none before the release opens the row", got)
			}
			if got := p.State.Get("COD-2", state.SkipsKey); got != state.EncodeSkips(tc.skips) {
				t.Errorf("sub-issue SKIPS = %q, want %q", got, state.EncodeSkips(tc.skips))
			}
		})
	}
}

// The release honors ci and merge — the only two keys that name work it does. A
// skipped gate is not polled at all rather than polled and waved through, and a
// skipped merge leaves the epic PR standing for the operator.
func TestEpicFinalizeHonorsCIAndMerge(t *testing.T) {
	cases := []struct {
		name       string
		skips      []string
		checks     []Check
		wantPolled bool
		wantMerged bool
	}{
		{
			name:       "nothing skipped ships through a green gate",
			checks:     []Check{{Name: "ci/test", Bucket: "pass"}},
			wantPolled: true,
			wantMerged: true,
		},
		{
			name:       "ci skipped ships past a red gate",
			skips:      []string{config.SkipCI},
			checks:     []Check{{Name: "ci/test", Bucket: "fail"}},
			wantMerged: true,
		},
		{
			name:       "merge skipped waits for the operator",
			skips:      []string{config.SkipMerge},
			checks:     []Check{{Name: "ci/test", Bucket: "pass"}},
			wantPolled: true,
		},
		{
			name:   "ci and merge skipped neither gates nor merges",
			skips:  []string{config.SkipCI, config.SkipMerge},
			checks: []Check{{Name: "ci/test", Bucket: "fail"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := doneEpicTracker()
			gh := &countingChecksWaitGitHub{waitGitHub: waitGitHub{
				epicGitHub: epicGitHub{createURL: "https://github.test/pr/42", checks: tc.checks},
				replies:    []prReply{{state: "OPEN"}, {state: "OPEN"}, {state: "MERGED"}},
			}}
			p := newEpicWaitPipeline(t, gh, tr)
			p.AutoMerge = true
			p.Skips = tc.skips

			if err := p.FinalizeEpic(context.Background()); err != nil {
				t.Fatalf("FinalizeEpic = %v, want nil", err)
			}
			if polled := gh.checksCalls > 0; polled != tc.wantPolled {
				t.Errorf("CI polled = %v (%d call(s)), want %v", polled, gh.checksCalls, tc.wantPolled)
			}
			if merged := gh.mergeCalls > 0; merged != tc.wantMerged {
				t.Errorf("trau merged the epic PR = %v, want %v", merged, tc.wantMerged)
			}
			// Either way the epic closes: the operator's own merge lands it just as
			// trau's does, so the release never ends half-recorded.
			if tr.setStatus != tracker.StageDone {
				t.Errorf("epic status = %q, want it closed once the PR merged", tr.setStatus)
			}
			if got := p.State.Get("COD-1", "PHASE"); got != state.Merged {
				t.Errorf("epic PHASE = %q, want %q", got, state.Merged)
			}
		})
	}
}

// A release trau may not merge is a defined, resumable end state before it ever
// blocks: the epic is parked on a human with its PR recorded, exactly as the
// single-ticket awaiting-merge path leaves a ticket, so a run killed inside the
// wait leaves the card linked and the queue able to settle it.
func TestSkipMergeParksTheEpicOnTheOperatorBeforeWaiting(t *testing.T) {
	tr := doneEpicTracker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gh := &waitGitHub{
		epicGitHub: epicGitHub{
			createURL: "https://github.test/pr/42",
			checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
		},
		replies: []prReply{{state: "OPEN"}},
		onCall:  func(call int) { cancel() },
	}
	p := newEpicWaitPipeline(t, gh, tr)
	p.AutoMerge = true
	p.Skips = []string{config.SkipMerge}

	if err := p.FinalizeEpic(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("FinalizeEpic = %v, want context.Canceled — a stop mid-wait is blameless", err)
	}
	for key, want := range map[string]string{
		"PHASE":     state.Releasing,
		"RELEASE":   state.ReleaseAwaitingHuman,
		"PR":        "42",
		"PR_URL":    "https://github.test/pr/42",
		"PR_STATUS": prStatusAwaitingMerge,
	} {
		if got := p.State.Get("COD-1", key); got != want {
			t.Errorf("epic %s = %q, want %q", key, got, want)
		}
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d time(s), want 0 — the operator merges a skip-merge release", gh.mergeCalls)
	}
}

// verify, lintfix and cleanup name QA, and the release does none: its sync with the
// base is delivery work — an agent resolving drift conflicts nothing else will —
// so it runs whatever the operator skipped.
func TestEpicFinalizeSyncRunsWhateverQAWasSkipped(t *testing.T) {
	cases := []struct {
		name  string
		skips []string
	}{
		{name: "nothing skipped"},
		{name: "verify skipped", skips: []string{config.SkipVerify}},
		{name: "every QA key skipped", skips: []string{config.SkipLintFix, config.SkipCleanup, config.SkipVerify}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &countingRunner{results: []error{nil}, name: "claude"}
			tr := doneEpicTracker()
			gh := &epicGitHub{createURL: "https://github.test/pr/42"}
			p := shippableEpicPipeline(t, gh, tr)
			p.Git = conflictedEpicGit{}
			p.Runner = runner
			p.PhaseLogs = newMemPhaseLogs()
			p.RunsDir = t.TempDir()
			p.MaxRepairs = 2
			p.Skips = tc.skips

			err := p.FinalizeEpic(context.Background())
			var handOff *EpicHandOffError
			if !errors.As(err, &handOff) {
				t.Fatalf("FinalizeEpic = %v, want an *EpicHandOffError — the conflict never cleared", err)
			}
			if runner.calls != p.MaxRepairs {
				t.Errorf("conflict-repair agent ran %d time(s), want %d — the sync is delivery work, not QA", runner.calls, p.MaxRepairs)
			}
			if gh.mergeCalls != 0 {
				t.Errorf("merged %d time(s) over an unresolved conflict, want 0", gh.mergeCalls)
			}
		})
	}
}

// A stacked epic replaces the epic PR's gate and merge with the stack's, so the two
// keys have to land on that path too: ci stops every layer being polled, merge
// leaves the whole stack standing for the operator.
func TestFinalizeStackedEpicHonorsCIAndMerge(t *testing.T) {
	cases := []struct {
		name       string
		skips      []string
		checks     []Check
		wantPolled bool
		wantMerged bool
	}{
		{
			name:       "nothing skipped merges a green stack",
			checks:     greenChecks,
			wantPolled: true,
			wantMerged: true,
		},
		{
			name:       "ci skipped merges a red stack",
			skips:      []string{config.SkipCI},
			checks:     []Check{{Name: "ci/test", Bucket: "fail"}},
			wantMerged: true,
		},
		{
			name:       "merge skipped waits on the operator",
			skips:      []string{config.SkipMerge},
			checks:     greenChecks,
			wantPolled: true,
		},
		{
			name:   "ci and merge skipped neither gates nor merges",
			skips:  []string{config.SkipCI, config.SkipMerge},
			checks: []Check{{Name: "ci/test", Bucket: "fail"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A release trau merges itself must find every layer open — a top PR
			// already reading merged short-circuits the stack merge. One it may not
			// merge has to see the operator land the stack after the layer checks,
			// which is two PRState calls in.
			flipAfter := 2
			if tc.wantMerged {
				flipAfter = 0
			}
			gh := &countingChecksStackedGitHub{stackedGitHub: stackedGitHub{flipMergedAfter: flipAfter}}
			gh.stacksEnabled, gh.checks = true, tc.checks
			tr := &epicTracker{title: "Stacked epic"}
			p := newStackedPipeline(t, newStackedGit(), gh, tr)
			p.AutoMerge = true
			p.Skips = tc.skips
			chain := seedStack(t, p, tr, 2)
			top := chain[len(chain)-1]

			if err := p.FinalizeEpic(context.Background()); err != nil {
				t.Fatalf("FinalizeEpic = %v, want nil", err)
			}
			if polled := gh.checksCalls > 0; polled != tc.wantPolled {
				t.Errorf("layer CI polled = %v (%d call(s)), want %v", polled, gh.checksCalls, tc.wantPolled)
			}
			if merged := gh.stackMergeCalls > 0; merged != tc.wantMerged {
				t.Errorf("trau merged the stack = %v, want %v", merged, tc.wantMerged)
			}
			if gh.mergeCalls != 0 {
				t.Errorf("the legacy merge endpoint ran %d time(s), want 0 on a stacked epic", gh.mergeCalls)
			}
			if got := p.State.Get(stackedEpicID, "PHASE"); got != state.Merged {
				t.Errorf("epic PHASE = %q, want %q once the stack landed", got, state.Merged)
			}
			if !tc.wantMerged {
				if got := p.State.Get(stackedEpicID, "PR"); got != top.PR {
					t.Errorf("epic PR = %q, want the stack's top PR %q for the operator to merge", got, top.PR)
				}
			}
		})
	}
}

// A skip is a property of the work, not of the invocation (ADR 0037), so a loop
// killed mid-epic and restarted with no argv behind it keeps honoring the set: the
// sub-issue that was in flight reads it off its own checkpoint, and a sibling the
// restarted loop reaches for the first time reads it off the epic's.
func TestEpicSkipsSurviveAKilledLoop(t *testing.T) {
	const (
		epic    = "COD-1"
		started = "COD-2"
		fresh   = "COD-3"
	)
	cases := []struct {
		name string
		id   string
		want []string
	}{
		{name: "the sub-issue that was in flight", id: started, want: []string{config.SkipVerify, config.SkipCI}},
		{name: "a sibling the restart reaches first", id: fresh, want: []string{config.SkipVerify, config.SkipCI}},
		{name: "the release itself", id: epic, want: []string{config.SkipVerify, config.SkipCI}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := epicRunPipeline(t, &countingRunner{results: []error{nil}, name: "claude"}, []string{config.SkipVerify, config.SkipCI})
			first.loadSkips(started)

			// The restart shares the checkpoint store and declares nothing.
			resumed := epicRunPipeline(t, &countingRunner{results: []error{nil}, name: "claude"}, nil)
			resumed.State = first.State
			resumed.loadSkips(tc.id)

			if !equalKeys(resumed.runSkips, tc.want) {
				t.Fatalf("effective skips = %v, want %v", resumed.runSkips, tc.want)
			}
			// Whatever it inherited lands on its own checkpoint, so the next restart
			// answers for it without going back to the epic.
			if got := resumed.State.Get(tc.id, state.SkipsKey); got != state.EncodeSkips(tc.want) {
				t.Errorf("%s recorded SKIPS = %q, want %q", tc.id, got, state.EncodeSkips(tc.want))
			}
		})
	}
}

// A ticket that is nobody's sub-issue never reads an epic's set, and a stored set
// only ever travels down: the release resolves against the epic alone, so a skip a
// sub-issue read back can never disarm the epic's gates.
func TestStoredSkipsTravelDownFromTheEpicOnly(t *testing.T) {
	cases := []struct {
		name       string
		epicID     string
		epicSkips  string
		childSkips string
		id         string
		want       []string
	}{
		{name: "sub-issue inherits the epic", epicID: "COD-1", epicSkips: "verify", id: "COD-2", want: []string{config.SkipVerify}},
		{name: "sub-issue's own set wins", epicID: "COD-1", epicSkips: "verify", childSkips: "merge", id: "COD-2", want: []string{config.SkipMerge}},
		{name: "release ignores a sub-issue's set", epicID: "COD-1", childSkips: "ci", id: "COD-1"},
		{name: "standalone ticket inherits nothing", epicSkips: "verify", id: "COD-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.EpicID = tc.epicID
			for id, keys := range map[string]string{"COD-1": tc.epicSkips, "COD-2": tc.childSkips} {
				if keys == "" {
					continue
				}
				if err := p.State.Set(id, state.SkipsKey, keys); err != nil {
					t.Fatal(err)
				}
			}

			p.loadSkips(tc.id)

			if !equalKeys(p.runSkips, tc.want) {
				t.Errorf("effective skips for %s = %v, want %v", tc.id, p.runSkips, tc.want)
			}
		})
	}
}

// The finalize-only resume: the loop died with every child delivered, and the run
// that picks the release up carries no argv. It reads ci and merge off the epic's
// own checkpoint — the gate stays down and the PR stays the operator's.
func TestFinalizeOnlyResumeHonorsTheRecordedCIAndMergeSkips(t *testing.T) {
	tr := doneEpicTracker()
	gh := &countingChecksWaitGitHub{waitGitHub: waitGitHub{
		epicGitHub: epicGitHub{
			createURL: "https://github.test/pr/42",
			checks:    []Check{{Name: "ci/test", Bucket: "fail"}},
		},
		replies: []prReply{{state: "OPEN"}, {state: "OPEN"}, {state: "MERGED"}},
	}}
	p := newEpicWaitPipeline(t, gh, tr)
	p.AutoMerge = true
	p.CIPoll = 1
	if err := p.State.Set("COD-1", state.SkipsKey, "ci,merge"); err != nil {
		t.Fatal(err)
	}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic = %v, want nil", err)
	}
	if !equalKeys(p.runSkips, []string{config.SkipCI, config.SkipMerge}) {
		t.Fatalf("release skips = %v, want the set recorded on the epic", p.runSkips)
	}
	if gh.checksCalls != 0 {
		t.Errorf("CI polled %d time(s), want 0 — the recorded set skips the gate", gh.checksCalls)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d time(s), want 0 — the operator merges a skip-merge release", gh.mergeCalls)
	}
	if tr.setStatus != tracker.StageDone {
		t.Errorf("epic status = %q, want it closed once the operator merged", tr.setStatus)
	}
}
