package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// rateLimited is the shape of a provider pause, used here only as a loud signal
// that an agent phase actually ran.
const rateLimited = "kimi run (verify): 429 usage limit reached"

// armSkips installs a run's declared skip set the way Resume does, so a probe
// exercises the same effective set production resolves.
func armSkips(p *Pipeline, id string, skips []string) {
	p.Skips = skips
	p.loadSkips(id)
}

// skipProbe reports what one skip set left standing: true means the work still
// ran. Each field is measured by its own pipeline, so a probe can never be
// answered by another probe's leftovers.
type skipProbe struct {
	lintFix, cleanup, verify, ciGate, merge bool
}

func probeSkips(t *testing.T, id string, skips []string) skipProbe {
	t.Helper()
	return skipProbe{
		lintFix: ranLintFix(t, id, skips),
		cleanup: ranCleanup(t, id, skips),
		verify:  ranVerify(t, id, skips),
		ciGate:  ranCIGate(t, id, skips),
		merge:   ranMerge(t, id, skips),
	}
}

func ranLintFix(t *testing.T, id string, skips []string) bool {
	t.Helper()
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.LintFix = true
	armSkips(p, id, skips)
	if err := p.lintFix(context.Background(), id); err != nil {
		t.Fatalf("lintFix = %v, want nil (it fails open)", err)
	}
	return runner.calls > 0
}

func ranCleanup(t *testing.T, id string, skips []string) bool {
	t.Helper()
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.Cleanup = true
	armSkips(p, id, skips)
	if err := p.cleanup(context.Background(), id); err != nil {
		t.Fatalf("cleanup = %v, want nil (it fails open)", err)
	}
	return runner.calls > 0
}

// ranVerify measures the Verify Step through a runner that pauses: a verifier
// that spawned surfaces the pause, and one that never spawned leaves the
// checkpoint at verified with no error at all.
func ranVerify(t *testing.T, id string, skips []string) bool {
	t.Helper()
	writeHandoff(t, id)
	p := newTestPipeline(t, fakeRunner{err: errors.New(rateLimited)}, &fakeTracker{})
	armSkips(p, id, skips)
	err := p.Verify(context.Background(), id)
	if err == nil {
		return false
	}
	if !IsPaused(err) {
		t.Fatalf("Verify = %v, want either nil (skipped) or a paused error (ran)", err)
	}
	return true
}

func ranCIGate(t *testing.T, id string, skips []string) bool {
	t.Helper()
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Delivery = &epicGitHub{checks: []Check{{Name: "build", Bucket: "fail"}}}
	p.Sleep = func(time.Duration) {}
	armSkips(p, id, skips)
	err := p.pollCI(context.Background(), "42", "main")
	if err == nil {
		return false
	}
	if !errors.Is(err, ErrCIFailed) {
		t.Fatalf("pollCI = %v, want either nil (skipped) or ErrCIFailed (ran)", err)
	}
	return true
}

// ranMerge drives the whole Ship close-out against a PR a human merges on the
// second poll, so the skipped run terminates instead of waiting forever. The
// answer is whether trau merged the PR itself.
func ranMerge(t *testing.T, id string, skips []string) bool {
	t.Helper()
	gh := &waitGitHub{replies: []prReply{{state: "OPEN"}, {state: "OPEN"}, {state: "MERGED"}}}
	p := newMergePipeline(t, &mergeGit{branch: "feature/" + id + "-x"}, gh, &fakeTracker{})
	armSkips(p, id, skips)
	seedPROpen(t, p, id, "42", "feature/"+id+"-x")
	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	return gh.mergeCalls > 0
}

// Each canonical key must bypass its own work and leave every other Activity
// exactly where it was — the whole promise of a per-run skip set (ADR 0037).
func TestSkipsBypassOnlyTheNamedWork(t *testing.T) {
	all := skipProbe{lintFix: true, cleanup: true, verify: true, ciGate: true, merge: true}
	cases := []struct {
		name  string
		skips []string
		want  skipProbe
	}{
		{name: "no skips runs everything", skips: nil, want: all},
		{name: "lintfix", skips: []string{config.SkipLintFix}, want: skipProbe{cleanup: true, verify: true, ciGate: true, merge: true}},
		{name: "cleanup", skips: []string{config.SkipCleanup}, want: skipProbe{lintFix: true, verify: true, ciGate: true, merge: true}},
		{name: "verify", skips: []string{config.SkipVerify}, want: skipProbe{lintFix: true, cleanup: true, ciGate: true, merge: true}},
		{name: "ci", skips: []string{config.SkipCI}, want: skipProbe{lintFix: true, cleanup: true, verify: true, merge: true}},
		{name: "merge", skips: []string{config.SkipMerge}, want: skipProbe{lintFix: true, cleanup: true, verify: true, ciGate: true}},
		{name: "verify and ci together", skips: []string{config.SkipVerify, config.SkipCI}, want: skipProbe{lintFix: true, cleanup: true, merge: true}},
		{name: "every key", skips: config.SkipKeys(), want: skipProbe{}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := skipTicket(t, i)
			if got := probeSkips(t, id, tc.skips); got != tc.want {
				t.Errorf("with --skip %v, work that ran = %+v, want %+v", tc.skips, got, tc.want)
			}
		})
	}
}

// skipTicket mints a ticket id unique to one subtest, since Verify's handoff
// brief and verdict live at a per-ticket path under /tmp.
func skipTicket(t *testing.T, n int) string {
	t.Helper()
	return "COD-9151" + string(rune('A'+n))
}

// A run that skips verify must reach the verified checkpoint without spending a
// single agent turn on handoff, the verifier, repair or bugfix — and must record
// no verdict, since none was graded.
func TestSkipVerifyAdvancesTheCheckpointWithoutAnAgent(t *testing.T) {
	id := "COD-91520"
	writeHandoff(t, id)
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	tr := &fakeTracker{}
	p := newTestPipeline(t, runner, tr)
	arts := newMemArtifacts()
	p.Artifacts = arts
	armSkips(p, id, []string{config.SkipVerify})

	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if runner.calls != 0 {
		t.Errorf("agent calls = %d, want 0 (no verifier, repair or bugfix turn)", runner.calls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Verified {
		t.Errorf("PHASE = %q, want %q so the run proceeds to commit/PR", got, state.Verified)
	}
	if _, ok, _ := arts.Get(id, artifactVerdict); ok {
		t.Error("a verdict was stored for a run that graded nothing")
	}
	if tr.fileBugCalls != 0 || tr.quarantineCalls != 0 {
		t.Errorf("FileBug=%d Quarantine=%d, want 0/0 — a skipped verify is not a failed one", tr.fileBugCalls, tr.quarantineCalls)
	}
}

// The handoff brief exists for the cold verifier alone, so a run that skips
// verify writes none — while the lint-fix and cleanup chain in front of it still
// runs and the handed_off checkpoint is still reached.
func TestSkipVerifyDropsTheHandoffButKeepsTheStyleChain(t *testing.T) {
	id := "COD-91521"
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.LintFix = true
	p.Cleanup = true
	armSkips(p, id, []string{config.SkipVerify})

	if err := p.handoffAndCleanup(context.Background(), id, true); err != nil {
		t.Fatalf("handoffAndCleanup = %v, want nil", err)
	}
	if runner.calls != 2 {
		t.Errorf("agent calls = %d, want 2 (lint-fix and cleanup, no handoff)", runner.calls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.HandedOff {
		t.Errorf("PHASE = %q, want %q", got, state.HandedOff)
	}
}

// A skipped verify must never read as a passed one: the PR body's Testing section
// and the ticket's QA note both state the bypass instead of any verify fact.
func TestSkipVerifyStatesTheBypassInThePRBodyAndQANote(t *testing.T) {
	id := "COD-91522"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	armSkips(p, id, []string{config.SkipVerify})

	body := p.prBody(context.Background(), id, "")
	if !strings.Contains(body, skippedVerifyLine) {
		t.Errorf("PR body Testing section = %q, want it to state the skipped verification", body)
	}
	for _, claim := range []string{"Verify passed", "No verify verdict was recorded"} {
		if strings.Contains(body, claim) {
			t.Errorf("PR body contains %q — a skipped verify must not report a verify outcome", claim)
		}
	}
	note := p.deliveryQANote(context.Background(), id, "https://x/pr/42")
	if !strings.Contains(note.Body, skippedVerifyLine) {
		t.Errorf("QA note = %q, want it to state the skipped verification", note.Body)
	}
	if !strings.Contains(note.Body, "https://x/pr/42") {
		t.Errorf("QA note = %q, want it to still carry the PR link", note.Body)
	}
}

// Skipping merge takes the AUTO_MERGE=0 path exactly: the PR is left open and
// stamped awaiting-merge, and trau never merges it itself.
func TestSkipMergeLeavesThePRForTheOperator(t *testing.T) {
	id := "COD-91523"
	gh := &waitGitHub{replies: []prReply{{state: "OPEN"}, {state: "OPEN"}, {state: "MERGED"}}}
	p := newMergePipeline(t, &mergeGit{branch: "feature/COD-91523-x"}, gh, &fakeTracker{})
	var buf bytes.Buffer
	p.Events = event.New(&buf)
	armSkips(p, id, []string{config.SkipMerge})
	seedPROpen(t, p, id, "42", "feature/COD-91523-x")

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0 — the operator merges a skip-merge run", gh.mergeCalls)
	}
	if evs := awaitingMergeEvents(t, &buf); len(evs) != 1 {
		t.Fatalf("emitted %d awaiting_merge events, want exactly 1", len(evs))
	}
}

// A Folder repo run merges through its own fan-out gate rather than the
// single-repo one, so it has to honor the skip too: every child PR stays open and
// trau merges none of them itself.
func TestSkipMergeLeavesEveryFolderChildPRForTheOperator(t *testing.T) {
	id := "COD-91529"
	root := t.TempDir()
	ghs := map[string]*epicGitHub{
		"api-companies": {checks: []Check{{Name: "ci/test", Bucket: "pass"}}},
		"api-users":     {checks: []Check{{Name: "ci/test", Bucket: "pass"}}},
	}
	for name := range ghs {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.AutoMerge = true
	p.RequireCI = config.CIGateOn
	p.GitAt = func(string) Git { return &childGit{} }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"SHIP_TARGETS": "api-companies,api-users",
		"PR_URLS":      "api-companies=https://github.com/acme/api-companies/pull/3,api-users=https://github.com/acme/api-users/pull/8",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}
	armSkips(p, id, []string{config.SkipMerge})

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	for name, gh := range ghs {
		if gh.mergeCalls != 0 {
			t.Errorf("%s: Merge called %d times, want 0 — the operator merges a skip-merge run", name, gh.mergeCalls)
		}
	}
	if got := p.State.Get(id, "PR_STATUS"); got != prStatusAwaitingMerge {
		t.Errorf("PR_STATUS = %q, want %q", got, prStatusAwaitingMerge)
	}
}

// The epic release is a merge trau performs too, so a run that skips merge leaves
// the epic PR standing for the operator instead of landing it on the base.
func TestSkipMergeLeavesTheEpicPRForTheOperator(t *testing.T) {
	tr := doneEpicTracker()
	gh := &waitGitHub{
		epicGitHub: epicGitHub{
			createURL: "https://github.test/pr/42",
			checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
		},
		replies: []prReply{{state: "OPEN"}, {state: "OPEN"}, {state: "MERGED"}},
	}
	p := newEpicWaitPipeline(t, gh, tr)
	p.AutoMerge = true
	p.Skips = []string{config.SkipMerge}

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic = %v, want nil", err)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0 — the operator merges a skip-merge run", gh.mergeCalls)
	}
	if tr.setStatus != tracker.StageDone {
		t.Errorf("epic status = %q, want it closed once the human merged the PR", tr.setStatus)
	}
}

// The release is the next unit of work the same Pipeline performs after a ticket,
// so it resolves its own skip set: a skip the last ticket read back from its
// checkpoint must never disarm the epic's own CI gate.
func TestEpicFinalizeDoesNotInheritATicketsStoredSkips(t *testing.T) {
	tr := doneEpicTracker()
	gh := &epicGitHub{
		createURL: "https://github.test/pr/42",
		checks:    []Check{{Name: "ci/test", Bucket: "fail"}},
	}
	p := shippableEpicPipeline(t, gh, tr)
	// The sub-issue resumed with no argv behind it, reading its skip off its own
	// checkpoint — the only way one ticket's set can reach the next unit of work.
	if err := p.State.Set("COD-2", state.SkipsKey, config.SkipCI); err != nil {
		t.Fatal(err)
	}
	p.loadSkips("COD-2")

	err := p.FinalizeEpic(context.Background())
	var handOff *EpicHandOffError
	if !errors.As(err, &handOff) {
		t.Fatalf("FinalizeEpic = %v, want a *EpicHandOffError — red epic CI ships nothing", err)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0 — the epic CI gate was armed", gh.mergeCalls)
	}
	if len(p.runSkips) != 0 {
		t.Errorf("the release inherited %v, want the sub-issue's stored set left behind", p.runSkips)
	}
}

// The declared set is recorded on the ticket's own checkpoint, a resume that
// carries no argv reads it back, and a set stored against one ticket never leaks
// onto the next ticket the same pipeline picks up.
func TestSkipsRoundTripThroughTheStateStore(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		stored   string
		want     []string
	}{
		{name: "declared set is honored and recorded", declared: []string{config.SkipVerify, config.SkipCI}, want: []string{config.SkipVerify, config.SkipCI}},
		{name: "resume with no argv reads the set back", stored: "verify,ci", want: []string{config.SkipVerify, config.SkipCI}},
		{name: "resume with argv keeps its own set", declared: []string{config.SkipMerge}, stored: "verify", want: []string{config.SkipMerge}},
		{name: "no set anywhere skips nothing", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-91524"
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			if tc.stored != "" {
				if err := p.State.Set(id, state.SkipsKey, tc.stored); err != nil {
					t.Fatal(err)
				}
			}
			armSkips(p, id, tc.declared)

			if got := p.runSkips; !equalKeys(got, tc.want) {
				t.Errorf("effective skips = %v, want %v", got, tc.want)
			}
			if len(tc.declared) > 0 {
				if got := p.State.Get(id, state.SkipsKey); got != state.EncodeSkips(tc.declared) {
					t.Errorf("recorded SKIPS = %q, want %q", got, state.EncodeSkips(tc.declared))
				}
			}
			// The next ticket the same pipeline picks up carries no stored set,
			// so it must come back to skipping nothing when argv declared nothing.
			p.Skips = nil
			p.loadSkips("COD-91525")
			if len(p.runSkips) != 0 {
				t.Errorf("a second ticket inherited %v, want no skips", p.runSkips)
			}
		})
	}
}

// A run resumed from its handed_off checkpoint with no argv behind it must still
// honor the skip its first attempt recorded — otherwise a verify-skipped run
// re-enters verify on the next pass and grades a diff nobody wrote a brief for.
func TestResumedVerifySkippedRunDoesNotReEnterVerify(t *testing.T) {
	id := "COD-91526"
	writeHandoff(t, id)
	first := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	armSkips(first, id, []string{config.SkipVerify})
	if err := first.Verify(context.Background(), id); err != nil {
		t.Fatalf("first Verify = %v, want nil", err)
	}

	// The resumed run shares the checkpoint store and declares nothing on argv.
	runner := &countingRunner{results: []error{errors.New(rateLimited)}, name: "claude"}
	resumed := newTestPipeline(t, runner, &fakeTracker{})
	resumed.State = first.State
	if err := resumed.State.Set(id, "PHASE", state.HandedOff); err != nil {
		t.Fatal(err)
	}
	resumed.loadSkips(id)

	if !resumed.skipping(config.SkipVerify) {
		t.Fatalf("resumed run skips %v, want the recorded verify skip", resumed.runSkips)
	}
	if err := resumed.Verify(context.Background(), id); err != nil {
		t.Fatalf("resumed Verify = %v, want nil", err)
	}
	if runner.calls != 0 {
		t.Errorf("resumed run spent %d agent call(s) on verify, want 0", runner.calls)
	}
	if got := resumed.State.Get(id, "PHASE"); got != state.Verified {
		t.Errorf("PHASE = %q, want %q", got, state.Verified)
	}
}

// The skip set is on the durable record: one event per ticket naming exactly what
// the operator bypassed, so a delivery no verifier saw can be accounted for later.
func TestSkipsEmitTheAuditEvent(t *testing.T) {
	id := "COD-91527"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	var buf bytes.Buffer
	p.Events = event.New(&buf)
	armSkips(p, id, []string{config.SkipVerify, config.SkipCI})

	evs := skipEvents(t, &buf)
	if len(evs) != 1 {
		t.Fatalf("emitted %d %s events, want exactly 1", len(evs), event.KindRunSkips)
	}
	if got := strField(evs[0].Fields, "ticket"); got != id {
		t.Errorf("ticket field = %q, want %q", got, id)
	}
	if !strings.Contains(evs[0].Msg, "verify,ci") {
		t.Errorf("message = %q, want it to name the skipped set", evs[0].Msg)
	}

	// A run that skips nothing says nothing.
	var quiet bytes.Buffer
	clean := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	clean.Events = event.New(&quiet)
	armSkips(clean, "COD-91528", nil)
	if evs := skipEvents(t, &quiet); len(evs) != 0 {
		t.Errorf("emitted %d %s events for a run with no skips, want 0", len(evs), event.KindRunSkips)
	}
}

func skipEvents(t *testing.T, buf *bytes.Buffer) []event.Event {
	t.Helper()
	var out []event.Event
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		if ev.Kind == event.KindRunSkips {
			out = append(out, ev)
		}
	}
	return out
}

func equalKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
