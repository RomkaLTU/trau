package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// steeredSession is a live terminal that stays open and records everything typed
// into it. onWrite lets a test end the run from the first keystroke it receives.
type steeredSession struct {
	mu      sync.Mutex
	typed   []byte
	onWrite func()
	done    chan struct{}
	once    sync.Once
}

func newSteeredSession() *steeredSession {
	return &steeredSession{done: make(chan struct{})}
}

func (s *steeredSession) Read([]byte) (int, error) { <-s.done; return 0, io.EOF }

func (s *steeredSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.typed = append(s.typed, p...)
	onWrite := s.onWrite
	s.mu.Unlock()
	if onWrite != nil {
		onWrite()
	}
	return len(p), nil
}

func (s *steeredSession) Wait() error  { <-s.done; return nil }
func (s *steeredSession) stop()        { s.once.Do(func() { close(s.done) }) }
func (s *steeredSession) Close() error { s.stop(); return nil }
func (s *steeredSession) Kill() error  { s.stop(); return nil }

func (s *steeredSession) transcript() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.typed)
}

type steerAck struct {
	id    int64
	phase string
}

// finishOnResultPath returns a starter that hands the run sess and reports the
// result path the backend told the agent to write, so the test can end the run
// whenever it likes.
func finishOnResultPath(t *testing.T, sess terminalSession, path *string) terminalStarter {
	t.Helper()
	return func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
		*path = resultPathFromPrompt(t, args)
		return sess, nil
	}
}

func writeResult(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("done"), 0o644); err != nil {
		t.Errorf("write result file: %v", err)
	}
}

// TestSteerNoteIsPastedAndAcked pins the mid-phase injection contract: a queued
// note is typed into the live session as a bracketed paste, then acked under the
// phase that took it.
func TestSteerNoteIsPastedAndAcked(t *testing.T) {
	sess := newSteeredSession()
	defer sess.stop()

	var resultPath string
	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		TrustPromptWait: time.Millisecond,
		steerPoll:       time.Millisecond,
		start:           finishOnResultPath(t, sess, &resultPath),
	}
	sess.onWrite = func() { writeResult(t, resultPath) }

	var (
		mu    sync.Mutex
		acks  []steerAck
		body  = "the test DB is on now\nuse it instead of the fixture"
		notes = []SteerNote{{ID: 7, Body: body}}
	)
	src := SteerSource{
		Ticket:  "COD-1",
		Pending: func(context.Context) ([]SteerNote, error) { return notes, nil },
		Ack: func(_ context.Context, id int64, phase string) error {
			mu.Lock()
			defer mu.Unlock()
			acks = append(acks, steerAck{id: id, phase: phase})
			return nil
		},
	}

	if _, err := c.Run(WithSteer(context.Background(), src), "do the thing", "build"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := "\x1b[200~" + body + "\x1b[201~\r"
	if got := sess.transcript(); !strings.Contains(got, want) {
		t.Errorf("session was typed %q, want a bracketed paste of the note", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(acks) != 1 {
		t.Fatalf("acked %d notes, want 1", len(acks))
	}
	if acks[0] != (steerAck{id: 7, phase: "build"}) {
		t.Errorf("ack = %+v, want note 7 acked under build", acks[0])
	}
}

// TestSteerHubErrorLeavesPhaseUnharmed pins the best-effort contract: a queue the
// child cannot read is swallowed and the phase completes untouched.
func TestSteerHubErrorLeavesPhaseUnharmed(t *testing.T) {
	sess := newSteeredSession()
	defer sess.stop()

	var resultPath string
	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		TrustPromptWait: time.Millisecond,
		steerPoll:       time.Millisecond,
		start:           finishOnResultPath(t, sess, &resultPath),
	}

	acked := false
	src := SteerSource{
		Ticket: "COD-2",
		Pending: func(context.Context) ([]SteerNote, error) {
			writeResult(t, resultPath)
			return nil, errors.New("hub unreachable")
		},
		Ack: func(context.Context, int64, string) error { acked = true; return nil },
	}

	if _, err := c.Run(WithSteer(context.Background(), src), "do the thing", "verify"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := sess.transcript(); got != "" {
		t.Errorf("session was typed %q on an unreadable queue, want nothing", got)
	}
	if acked {
		t.Error("acked a note that was never read")
	}
}

// TestSteerNoteTypedOnceDespiteAckFailure guards against re-injection: a failed
// ack leaves the note pending on the hub, but it is already in the terminal.
func TestSteerNoteTypedOnceDespiteAckFailure(t *testing.T) {
	sess := newSteeredSession()
	defer sess.stop()

	var resultPath string
	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		TrustPromptWait: time.Millisecond,
		steerPoll:       time.Millisecond,
		start:           finishOnResultPath(t, sess, &resultPath),
	}

	var polls int
	src := SteerSource{
		Ticket: "COD-3",
		Pending: func(context.Context) ([]SteerNote, error) {
			if polls++; polls >= 3 {
				writeResult(t, resultPath)
			}
			return []SteerNote{{ID: 9, Body: "check the staging URL"}}, nil
		},
		Ack: func(context.Context, int64, string) error { return errors.New("hub unreachable") },
	}

	if _, err := c.Run(WithSteer(context.Background(), src), "do the thing", "repair1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := strings.Count(sess.transcript(), "check the staging URL"); n != 1 {
		t.Errorf("note typed %d times across %d polls, want exactly 1", n, polls)
	}
}
