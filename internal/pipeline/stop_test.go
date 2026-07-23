package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
)

// signalledContext mirrors main's root context: it registers the loop's signal
// handler, raises sig at this process, and returns once the cancellation has
// landed — so the pipeline sees exactly what a web Stop or a Ctrl-C leaves behind.
func signalledContext(t *testing.T, sig syscall.Signal) context.Context {
	t.Helper()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	t.Cleanup(stop)
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatalf("raise %v: %v", sig, err)
	}
	<-ctx.Done()
	return ctx
}

// TestDeliberateStopClassifiesAsStopped is the COD-1096 guard: cancelling the
// loop's root context is an operator ending the run, not the ticket breaking, so
// it must record the blameless stopped class and unwrap to context.Canceled for
// the CLI's signal path. An AGENT_TIMEOUT expires a CHILD context while the root
// stays live, and must keep faulting — as must any ordinary phase error.
func TestDeliberateStopClassifiesAsStopped(t *testing.T) {
	live := func(*testing.T) context.Context { return context.Background() }

	tests := []struct {
		name       string
		ctx        func(*testing.T) context.Context
		agentErr   error
		wantClass  string
		wantState  string
		wantReason string
	}{
		{
			name:       "sigterm mid-build stops",
			ctx:        func(t *testing.T) context.Context { return signalledContext(t, syscall.SIGTERM) },
			agentErr:   fmt.Errorf("claude interactive run (build): %w", context.Canceled),
			wantClass:  state.FailStopped,
			wantState:  "stopped",
			wantReason: "stopped during build — work saved at the last checkpoint",
		},
		{
			name:       "ctrl-c mid-build stops",
			ctx:        func(t *testing.T) context.Context { return signalledContext(t, syscall.SIGINT) },
			agentErr:   fmt.Errorf("claude interactive run (build): %w", context.Canceled),
			wantClass:  state.FailStopped,
			wantState:  "stopped",
			wantReason: "stopped during build — work saved at the last checkpoint",
		},
		{
			name:       "agent timeout still faults",
			ctx:        live,
			agentErr:   fmt.Errorf("claude interactive run (build): %w", context.DeadlineExceeded),
			wantClass:  state.FailFaulted,
			wantState:  "faulted",
			wantReason: "unexpected error during build: claude interactive run (build): context deadline exceeded",
		},
		{
			name:       "real phase error still faults",
			ctx:        live,
			agentErr:   errors.New("kimi run (build): process exited unexpectedly"),
			wantClass:  state.FailFaulted,
			wantState:  "faulted",
			wantReason: "unexpected error during build: kimi run (build): process exited unexpectedly",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-1096"
			var buf bytes.Buffer
			git := &recordingGit{branch: "feature/COD-1096-x"}
			p := newTestPipeline(t, fakeRunner{err: tc.agentErr}, &fakeTracker{})
			p.Git = git
			p.Remote = "origin"
			p.Events = event.New(&buf)
			if err := p.State.Set(id, "BRANCH", git.branch); err != nil {
				t.Fatal(err)
			}

			err := p.Process(tc.ctx(t), id)

			if got := p.State.Get(id, "FAILURE_CLASS"); got != tc.wantClass {
				t.Fatalf("FAILURE_CLASS = %q, want %q", got, tc.wantClass)
			}
			if got := p.State.Get(id, "FAILURE_REASON"); got != tc.wantReason {
				t.Errorf("FAILURE_REASON = %q, want %q", got, tc.wantReason)
			}
			if got := p.State.Get(id, "PHASE"); got != state.Building {
				t.Errorf("PHASE = %q, want it left at its checkpoint (%q) so the ticket resumes", got, state.Building)
			}
			evs := stateChangeEvents(t, &buf)
			if len(evs) != 1 {
				t.Fatalf("emitted %d state_change events, want exactly 1: %v", len(evs), evs)
			}
			if got := strField(evs[0].Fields, "state"); got != tc.wantState {
				t.Errorf("state_change state = %q, want %q", got, tc.wantState)
			}
			if git.count("commit") != 1 || !strings.Contains(git.commitMsgs[0], id) {
				t.Errorf("WIP not preserved: commits = %v", git.commitMsgs)
			}
			if git.count("clean") == 0 {
				t.Error("expected the working tree to be cleaned back to base")
			}
			if len(git.deadCtx) != 0 {
				t.Errorf("%v ran on the cancelled run context — the cleanup must be detached", git.deadCtx)
			}

			if tc.wantClass == state.FailFaulted {
				if !IsFault(err) {
					t.Fatalf("Process err = %v, want a *FaultError", err)
				}
				if IsStopped(err) {
					t.Errorf("a genuine failure must not read as a deliberate stop: %v", err)
				}
				return
			}
			if !IsStopped(err) {
				t.Fatalf("Process err = %v, want a *StoppedError", err)
			}
			if IsFault(err) {
				t.Errorf("a deliberate stop must not fault: %v", err)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("a stop must unwrap to context.Canceled so the CLI exits 130: %v", err)
			}
		})
	}
}

// TestResumeClearsTheStoppedMarker proves the stopped class is as transient as
// every other: the next attempt drops it, so a resumed ticket stops reading as
// stopped the moment it runs again.
func TestResumeClearsTheStoppedMarker(t *testing.T) {
	id := "COD-1096"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	if err := p.State.Set(id, "FAILURE_CLASS", state.FailStopped); err != nil {
		t.Fatal(err)
	}
	if err := p.State.Set(id, "FAILURE_REASON", "stopped during build — work saved at the last checkpoint"); err != nil {
		t.Fatal(err)
	}

	p.clearFailureMarks(id)

	if got := p.State.Get(id, "FAILURE_CLASS"); got != "" {
		t.Errorf("FAILURE_CLASS = %q, want it cleared", got)
	}
	if got := p.State.Get(id, "FAILURE_REASON"); got != "" {
		t.Errorf("FAILURE_REASON = %q, want it cleared", got)
	}
}
