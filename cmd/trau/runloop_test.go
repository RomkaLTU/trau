package main

import (
	"context"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
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
