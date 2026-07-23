package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
)

// waitGitHub scripts the PRState replies of the AUTO_MERGE=0 manual-merge wait:
// each poll pops the next reply, repeating the last one once the script runs out.
// onCall fires before each reply so a test can cancel the context mid-wait.
type waitGitHub struct {
	epicGitHub
	replies []prReply
	calls   int
	onCall  func(call int)
}

type prReply struct {
	state string
	err   error
}

func (g *waitGitHub) PRState(context.Context, string) (string, error) {
	i := g.calls
	g.calls++
	if g.onCall != nil {
		g.onCall(i)
	}
	if i >= len(g.replies) {
		i = len(g.replies) - 1
	}
	return g.replies[i].state, g.replies[i].err
}

func newWaitPipeline(t *testing.T, gh GitHub, tr *fakeTracker) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	return &Pipeline{
		Runner:      fakeRunner{},
		Tracker:     tr,
		Git:         fakeGit{},
		GitHub:      gh,
		State:       state.NewStore(dir),
		RunsDir:     dir,
		Base:        "main",
		Remote:      "origin",
		Prefix:      "COD",
		MergeMethod: "squash",
		Sleep:       func(time.Duration) {},
	}
}

func awaitingMergeEvents(t *testing.T, buf *bytes.Buffer) []event.Event {
	t.Helper()
	var out []event.Event
	for _, ev := range stateChangeEvents(t, buf) {
		if strField(ev.Fields, "state") == "awaiting_merge" {
			out = append(out, ev)
		}
	}
	return out
}

// With AUTO_MERGE=0, a green PR the operator merges by hand is marked Done exactly
// like the auto path, and the wait announces itself once through the notification
// pathway carrying the PR number and URL.
func TestCIAndMergeManualMergeWaitsThenDone(t *testing.T) {
	id := "COD-1117A"
	gh := &waitGitHub{replies: []prReply{{state: "OPEN"}, {state: "OPEN"}, {state: "MERGED"}}}
	tr := &fakeTracker{}
	p := newWaitPipeline(t, gh, tr)
	var buf bytes.Buffer
	p.Events = event.New(&buf)
	seedPROpen(t, p, id, "42", "feature/COD-1117A-x")

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged", got)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0 (the human merges under AUTO_MERGE=0)", gh.mergeCalls)
	}

	evs := awaitingMergeEvents(t, &buf)
	if len(evs) != 1 {
		t.Fatalf("emitted %d awaiting_merge events, want exactly 1", len(evs))
	}
	if got := strField(evs[0].Fields, "pr"); got != "42" {
		t.Errorf("pr field = %q, want 42", got)
	}
	if got := strField(evs[0].Fields, "url"); got != "https://x/pr/42" {
		t.Errorf("url field = %q, want the PR url", got)
	}
	if got := strField(evs[0].Fields, "ticket"); got != id {
		t.Errorf("ticket field = %q, want %q", got, id)
	}
}

// A PR closed without merging is an explicit human rejection: give up (quarantine +
// needs-human) with a reason that names the PR, and let the loop keep going.
func TestCIAndMergeManualMergeClosedGivesUp(t *testing.T) {
	id := "COD-1117B"
	gh := &waitGitHub{replies: []prReply{{state: "OPEN"}, {state: "CLOSED"}}}
	tr := &fakeTracker{}
	p := newWaitPipeline(t, gh, tr)
	seedPROpen(t, p, id, "43", "feature/COD-1117B-x")

	err := p.CIAndMerge(context.Background(), id)
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	if !strings.Contains(g.Reason, "PR #43 closed without merge") {
		t.Errorf("give-up reason = %q, want it to name the closed PR", g.Reason)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine called %d times, want 1", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want quarantined", got)
	}
}

// A context canceled mid-wait is a blameless stop: CIAndMerge propagates the
// cancellation, the checkpoint is preserved at pr_open, and the classifier routes
// it to the stopped class rather than quarantining.
func TestCIAndMergeManualMergeContextCancelStops(t *testing.T) {
	id := "COD-1117C"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gh := &waitGitHub{
		replies: []prReply{{state: "OPEN"}},
		onCall: func(call int) {
			if call == 1 {
				cancel()
			}
		},
	}
	tr := &fakeTracker{}
	p := newWaitPipeline(t, gh, tr)
	seedPROpen(t, p, id, "44", "feature/COD-1117C-x")

	err := p.CIAndMerge(ctx, id)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CIAndMerge = %v, want context.Canceled", err)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0 (a stop is blameless)", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.PROpen {
		t.Errorf("PHASE = %q, want pr_open preserved", got)
	}

	var s *StoppedError
	if out := p.classifyPhaseErr(ctx, id, err); !errors.As(out, &s) {
		t.Errorf("classifyPhaseErr = %v, want a *StoppedError", out)
	}
}

// A transient PRState lookup failure must not end the wait — polling continues and
// converges once the PR actually merges.
func TestCIAndMergeManualMergeTransientErrorKeepsWaiting(t *testing.T) {
	id := "COD-1117D"
	boom := errors.New("gh api: dial tcp: i/o timeout")
	gh := &waitGitHub{replies: []prReply{
		{state: "OPEN"},
		{err: boom},
		{err: boom},
		{state: "MERGED"},
	}}
	tr := &fakeTracker{}
	p := newWaitPipeline(t, gh, tr)
	seedPROpen(t, p, id, "45", "feature/COD-1117D-x")

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil (the wait rode out the transient errors)", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged", got)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
	}
}
