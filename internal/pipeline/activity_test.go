package pipeline

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

// activityRecorder captures the present-tense activity reports in order, safe for
// the concurrent build tail where lintfix, cleanup, and handoff fire from separate
// goroutines.
type activityRecorder struct {
	mu   sync.Mutex
	seen []reportedActivity
}

type reportedActivity struct {
	id       string
	activity string
	detail   string
}

func (r *activityRecorder) hook() func(id, activity, detail string) {
	return func(id, act, detail string) {
		r.mu.Lock()
		r.seen = append(r.seen, reportedActivity{id, act, detail})
		r.mu.Unlock()
	}
}

func (r *activityRecorder) activities() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.seen))
	for i, a := range r.seen {
		out[i] = a.activity
	}
	return out
}

func (r *activityRecorder) has(act string) bool {
	for _, a := range r.activities() {
		if a == act {
			return true
		}
	}
	return false
}

// TestSetActivityReportsAndEmits pins the single writer: each call advances the
// presence heartbeat through OnActivity and lands exactly one activity_change event
// carrying the ticket, the activity, and the raw call label as detail (omitted when
// empty). Durations derive from these event timestamps, so one event per start is
// the contract.
func TestSetActivityReportsAndEmits(t *testing.T) {
	var buf bytes.Buffer
	rec := &activityRecorder{}
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Events = event.New(&buf)
	p.OnActivity = rec.hook()

	p.setActivity("COD-1", activity.Verify, "")
	p.setActivity("COD-1", activity.Repair, "repair2")

	if got := rec.seen; len(got) != 2 {
		t.Fatalf("OnActivity fired %d times, want 2", len(got))
	}
	if got := rec.seen[1]; got.id != "COD-1" || got.activity != "repair" || got.detail != "repair2" {
		t.Errorf("second report = %+v, want COD-1/repair/repair2", got)
	}

	evs := kindEvents(t, &buf, "activity_change")
	if len(evs) != 2 {
		t.Fatalf("emitted %d activity_change events, want 2", len(evs))
	}
	if got := strField(evs[0].Fields, "activity"); got != "verify" {
		t.Errorf("first event activity = %q, want verify", got)
	}
	if _, ok := evs[0].Fields["detail"]; ok {
		t.Errorf("first event carried a detail, want none for an empty label")
	}
	if got := strField(evs[1].Fields, "ticket"); got != "COD-1" {
		t.Errorf("second event ticket = %q, want COD-1", got)
	}
	if got := strField(evs[1].Fields, "detail"); got != "repair2" {
		t.Errorf("second event detail = %q, want repair2", got)
	}
}

// verifyScriptRunner grades the verify family (verify, verify-retryN) fail until the
// passOn-th attempt, so a Verify run walks the repair→bugfix ladder deterministically.
type verifyScriptRunner struct {
	mu      sync.Mutex
	verdict string
	runs    int
	passOn  int
}

func (r *verifyScriptRunner) Run(_ context.Context, _ string, phase string) (agent.Result, error) {
	if strings.HasPrefix(phase, "verify") {
		r.mu.Lock()
		r.runs++
		pass := r.runs >= r.passOn
		r.mu.Unlock()
		body := `{"pass":false,"summary":"nope","failures":["a check failed"]}`
		if pass {
			body = `{"pass":true,"summary":"ok","failures":[]}`
		}
		if err := os.WriteFile(r.verdict, []byte(body), 0o644); err != nil {
			return agent.Result{}, err
		}
	}
	return agent.Result{Final: phase + "-ok"}, nil
}

// TestVerifyReportsActivitySequence walks a real Verify through one repair then one
// bugfix before it passes, and pins the reported activity stream: verify → repair
// (repair1) → verify → bugfix (bugfix1) → verify. Each re-verify re-asserts the verify
// activity so the timeline shows the alternation the duration derivation reads.
func TestVerifyReportsActivitySequence(t *testing.T) {
	id := "COD-ACT-SEQ"
	writeHandoff(t, id)
	rec := &activityRecorder{}
	p := newTestPipeline(t, &verifyScriptRunner{verdict: verifyPath(id), passOn: 3}, &fakeTracker{})
	p.MaxRepairs = 1
	p.MaxBugfixes = 1
	p.OnActivity = rec.hook()

	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify = %v, want nil (passes on the third attempt)", err)
	}

	want := []string{"verify", "repair", "verify", "bugfix", "verify"}
	got := rec.activities()
	if len(got) != len(want) {
		t.Fatalf("activity sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("activity sequence = %v, want %v", got, want)
		}
	}
	for _, a := range rec.seen {
		switch {
		case a.activity == "repair" && a.detail != "repair1":
			t.Errorf("repair detail = %q, want repair1", a.detail)
		case a.activity == "bugfix" && a.detail != "bugfix1":
			t.Errorf("bugfix detail = %q, want bugfix1", a.detail)
		}
	}
}

// TestConcurrentBuildTailReportsAllActivities exercises the overlapped build tail —
// the handoff brief runs concurrently with the lintfix→cleanup chain — and confirms
// every one of its activities is reported (last-started wins for the heartbeat, but
// each still emits). Run under -race, it also guards the writer's concurrency safety.
func TestConcurrentBuildTailReportsAllActivities(t *testing.T) {
	id := "COD-ACT-TAIL"
	writeHandoff(t, id)
	rec := &activityRecorder{}
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.LintFix = true
	p.Cleanup = true
	p.OnActivity = rec.hook()

	if err := p.handoffAndCleanup(context.Background(), id, true); err != nil {
		t.Fatalf("handoffAndCleanup = %v, want nil", err)
	}

	for _, act := range []string{"handoff", "lintfix", "cleanup"} {
		if !rec.has(act) {
			t.Errorf("activity %q was not reported; got %v", act, rec.activities())
		}
	}
}
