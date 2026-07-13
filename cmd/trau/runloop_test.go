package main

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/hubpresence"
	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestRunLoopFinalizesOnCleanStop(t *testing.T) {
	eng := &loopEngine{}
	processed, err := runLoop(context.Background(), eng, loopParams{Max: 1, ParentSuffix: " under COD-1"}, noopRenderer{}, func(id string, elapsed time.Duration) console.TicketResult {
		t.Fatalf("result should not be called when no ticket is picked")
		return console.TicketResult{}
	})
	if err != nil {
		t.Fatalf("runLoop returned error: %v", err)
	}
	if len(processed) != 0 {
		t.Fatalf("expected no processed tickets, got %v", processed)
	}
	if !eng.finalized {
		t.Fatal("expected runLoop to finalize on clean stop")
	}
}

type loopEngine struct {
	finalized bool
}

func (l *loopEngine) ResumeTarget() (string, string)                  { return "", "" }
func (l *loopEngine) InferredResume(context.Context) (string, string) { return "", "" }
func (l *loopEngine) EnsureCleanBase(context.Context) error           { return nil }
func (l *loopEngine) RestoreWIP(context.Context)                      {}
func (l *loopEngine) Pick(context.Context) (string, error)            { return "", nil }
func (l *loopEngine) Process(context.Context, string, string) error   { return nil }
func (l *loopEngine) Finalize(context.Context) error {
	l.finalized = true
	return nil
}
func (l *loopEngine) BudgetExhausted() (string, bool) { return "", false }

type noopRenderer struct{}

func (noopRenderer) Logf(string, ...any)                {}
func (noopRenderer) LoopDone(console.SessionSummary)    {}
func (noopRenderer) Event(event.Event)                  {}
func (noopRenderer) Spin(string) func()                 { return func() {} }
func (noopRenderer) SetTicket(string)                   {}
func (noopRenderer) SetTitle(string)                    {}
func (noopRenderer) Activity(activity.Activity, string) {}
func (noopRenderer) TicketDone(console.TicketResult)    {}
func (noopRenderer) Wait()                              {}

type stateRec struct{ state, ticket, phase string }

func recorder(recs *[]stateRec) func(string, string, string) {
	return func(state, ticket, phase string) {
		*recs = append(*recs, stateRec{state, ticket, phase})
	}
}

// TestRunLoopReportsGrazingThenWorking pins the loop-engine transition mapping:
// grazing before each fresh pick, working when a ticket is picked up, and a
// trailing grazing when the queue drains.
func TestRunLoopReportsGrazingThenWorking(t *testing.T) {
	eng := &alreadyDoneEngine{picks: []string{"COD-1", "COD-2"}}
	var recs []stateRec
	_, err := runLoop(context.Background(), eng, loopParams{Max: 5, Report: recorder(&recs)}, noopRenderer{}, func(id string, _ time.Duration) console.TicketResult {
		return console.TicketResult{ID: id}
	})
	if err != nil {
		t.Fatalf("runLoop returned error: %v", err)
	}
	want := []stateRec{
		{registry.StateGrazing, "", ""},
		{registry.StateWorking, "COD-1", ""},
		{registry.StateGrazing, "", ""},
		{registry.StateWorking, "COD-2", ""},
		{registry.StateGrazing, "", ""},
	}
	if !reflect.DeepEqual(recs, want) {
		t.Fatalf("transition sequence = %v, want %v", recs, want)
	}
}

// TestRunLoopReportsWorkingOnResume: a resumed ticket goes straight to working
// with its checkpoint phase — no grazing, since the loop isn't picking.
func TestRunLoopReportsWorkingOnResume(t *testing.T) {
	eng := &resumeOnceEngine{}
	var recs []stateRec
	_, err := runLoop(context.Background(), eng, loopParams{Max: 1, Report: recorder(&recs)}, noopRenderer{}, func(id string, _ time.Duration) console.TicketResult {
		return console.TicketResult{ID: id}
	})
	if err != nil {
		t.Fatalf("runLoop returned error: %v", err)
	}
	want := []stateRec{{registry.StateWorking, "COD-9", "built"}}
	if !reflect.DeepEqual(recs, want) {
		t.Fatalf("transition sequence = %v, want %v", recs, want)
	}
}

// TestRunLoopReportsStoppingOnCancel: a cancelled context stops the loop and
// reports stopping before returning.
func TestRunLoopReportsStoppingOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var recs []stateRec
	_, err := runLoop(ctx, &loopEngine{}, loopParams{Max: 5, Report: recorder(&recs)}, noopRenderer{}, func(string, time.Duration) console.TicketResult {
		return console.TicketResult{}
	})
	if err != nil {
		t.Fatalf("runLoop returned error: %v", err)
	}
	if len(recs) != 1 || recs[0].state != registry.StateStopping {
		t.Fatalf("expected a single stopping report, got %v", recs)
	}
}

// fakePresenceClient records the last heartbeat a presence handle flushed, so a
// test can assert what session state the loop reported without a live hub.
type fakePresenceClient struct {
	mu    sync.Mutex
	last  hubclient.InstanceHeartbeat
	calls int
}

func (f *fakePresenceClient) PutInstance(_ context.Context, _ int, hb hubclient.InstanceHeartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = hb
	f.calls++
	return nil
}

func (f *fakePresenceClient) DeleteInstance(context.Context, int) error { return nil }

func (f *fakePresenceClient) snapshot() (hubclient.InstanceHeartbeat, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, f.calls
}

// TestReportAfterRunParks pins the single-ticket recap mapping: a fault, pause,
// or give-up parks the recap-alive session on its ticket, while a clean finish
// falls back to idle. The reported state reaches the hub through the presence
// handle's background heartbeat, so the test waits for the flush.
func TestReportAfterRunParks(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantState  string
		wantTicket string
	}{
		{"clean", nil, registry.StateIdle, ""},
		{"fault", &pipeline.FaultError{ID: "COD-1"}, registry.StateParked, "COD-1"},
		{"pause", &pipeline.PausedError{ID: "COD-2"}, registry.StateParked, "COD-2"},
		{"giveup", &pipeline.GiveUpError{ID: "COD-3"}, registry.StateParked, "COD-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakePresenceClient{}
			h := hubpresence.Register(fake, "/repo/acme", "")
			defer h.Deregister()
			(&appActions{reg: h}).reportAfterRun(tc.err)

			deadline := time.Now().Add(time.Second)
			var got hubclient.InstanceHeartbeat
			for {
				hb, calls := fake.snapshot()
				got = hb
				if calls > 0 && hb.SessionState == tc.wantState && hb.Ticket == tc.wantTicket {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("reportAfterRun(%v) reported {%s %s} not {%s %s} within 1s", tc.err, hb.SessionState, hb.Ticket, tc.wantState, tc.wantTicket)
				}
				time.Sleep(2 * time.Millisecond)
			}
			if got.Phase != "" {
				t.Errorf("reportAfterRun(%v) reported phase %q, want empty", tc.err, got.Phase)
			}
		})
	}
}

type resumeOnceEngine struct {
	loopEngine
	resumed bool
}

func (e *resumeOnceEngine) ResumeTarget() (string, string) {
	if e.resumed {
		return "", ""
	}
	e.resumed = true
	return "COD-9", "built"
}

func TestLeafSubsFiltersNestedEpics(t *testing.T) {
	subs := []tracker.SubIssue{
		{ID: "COD-500", HasChildren: true},
		{ID: "COD-501", HasChildren: false},
		{ID: "COD-502"},
	}
	got := leafSubs(subs)
	if len(got) != 2 {
		t.Fatalf("expected 2 leaf sub-issues, got %d", len(got))
	}
	for _, s := range got {
		if s.ID == "COD-500" {
			t.Fatalf("nested epic COD-500 should be filtered out")
		}
	}
}

// alreadyDoneEngine feeds a scripted pick sequence; Process returns
// ErrAlreadyDone for ids in done and succeeds otherwise.
type alreadyDoneEngine struct {
	loopEngine
	picks   []string
	done    map[string]bool
	handled []string
}

func (e *alreadyDoneEngine) Pick(context.Context) (string, error) {
	if len(e.picks) == 0 {
		return "", nil
	}
	id := e.picks[0]
	e.picks = e.picks[1:]
	return id, nil
}

func (e *alreadyDoneEngine) Process(_ context.Context, id, _ string) error {
	e.handled = append(e.handled, id)
	if e.done[id] {
		return pipeline.ErrAlreadyDone
	}
	return nil
}

// TestRunLoopSkipsAlreadyDonePick is the COD-708 regression guard: a picked
// ticket whose checkpoint is already merged must be skipped (not counted, not
// faulted) and the loop must keep going.
func TestRunLoopSkipsAlreadyDonePick(t *testing.T) {
	eng := &alreadyDoneEngine{picks: []string{"COD-1", "COD-2"}, done: map[string]bool{"COD-1": true}}
	processed, err := runLoop(context.Background(), eng, loopParams{Max: 5}, noopRenderer{}, func(id string, elapsed time.Duration) console.TicketResult {
		return console.TicketResult{ID: id}
	})
	if err != nil {
		t.Fatalf("runLoop returned error: %v", err)
	}
	if len(processed) != 1 || processed[0] != "COD-2" {
		t.Fatalf("expected only COD-2 processed, got %v", processed)
	}
	if !eng.finalized {
		t.Fatal("expected runLoop to finalize")
	}
}

// TestRunLoopStopsWhenDonePickRepeats: if pick offers the same already-done id
// twice, the tracker is not converging — stop cleanly instead of spending a
// pick agent per spin.
func TestRunLoopStopsWhenDonePickRepeats(t *testing.T) {
	eng := &alreadyDoneEngine{picks: []string{"COD-1", "COD-1", "COD-3"}, done: map[string]bool{"COD-1": true}}
	processed, err := runLoop(context.Background(), eng, loopParams{Max: 5}, noopRenderer{}, func(id string, elapsed time.Duration) console.TicketResult {
		return console.TicketResult{ID: id}
	})
	if err != nil {
		t.Fatalf("runLoop returned error: %v", err)
	}
	if len(processed) != 0 {
		t.Fatalf("expected no processed tickets, got %v", processed)
	}
	if len(eng.handled) != 2 || eng.handled[0] != "COD-1" || eng.handled[1] != "COD-1" {
		t.Fatalf("expected exactly two COD-1 attempts before stopping, got %v", eng.handled)
	}
	if !eng.finalized {
		t.Fatal("expected runLoop to finalize on clean stop")
	}
}
