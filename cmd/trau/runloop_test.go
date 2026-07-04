package main

import (
	"context"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/pipeline"
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
func (l *loopEngine) Pick(context.Context) (string, error)            { return "", nil }
func (l *loopEngine) Process(context.Context, string, string) error   { return nil }
func (l *loopEngine) Finalize(context.Context) error {
	l.finalized = true
	return nil
}
func (l *loopEngine) BudgetExhausted() (string, bool) { return "", false }

type noopRenderer struct{}

func (noopRenderer) Logf(string, ...any)             {}
func (noopRenderer) LoopDone(console.SessionSummary) {}
func (noopRenderer) Event(event.Event)               {}
func (noopRenderer) Spin(string) func()              { return func() {} }
func (noopRenderer) SetTicket(string)                {}
func (noopRenderer) SetTitle(string)                 {}
func (noopRenderer) PhaseStart(string)               {}
func (noopRenderer) TicketDone(console.TicketResult) {}
func (noopRenderer) Wait()                           {}

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
