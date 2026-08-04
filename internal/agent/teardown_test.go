package agent

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// teardownTestGrace keeps the backstop window short enough that a hung session is
// killed inside the test rather than after the shipped grace.
const teardownTestGrace = 20 * time.Millisecond

// teardownSession records the order the phase-end teardown calls its terminal in.
// Its first read announces kimi's composer, so one fake drives all three backends;
// with hangs set it ignores the hangup Close delivers — the child wedged mid-write
// that only Kill can end.
type teardownSession struct {
	mu      sync.Mutex
	calls   []string
	typed   []byte
	onWrite func(typed string)
	hangs   bool
	sent    bool
	exit    chan struct{}
	once    sync.Once
}

func newTeardownSession() *teardownSession {
	return &teardownSession{exit: make(chan struct{})}
}

func (s *teardownSession) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		return copy(p, kimiComposerReady), nil
	}
	<-s.exit
	return 0, io.EOF
}

func (s *teardownSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.typed = append(s.typed, p...)
	typed, onWrite := string(s.typed), s.onWrite
	s.mu.Unlock()
	if onWrite != nil {
		onWrite(typed)
	}
	return len(p), nil
}

func (s *teardownSession) Wait() error { <-s.exit; return nil }

func (s *teardownSession) Close() error {
	s.record("close")
	if !s.hangs {
		s.stop()
	}
	return nil
}

func (s *teardownSession) Kill() error {
	s.record("kill")
	s.stop()
	return nil
}

func (s *teardownSession) stop() { s.once.Do(func() { close(s.exit) }) }

func (s *teardownSession) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *teardownSession) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *teardownSession) killed() bool {
	for _, call := range s.order() {
		if call == "kill" {
			return true
		}
	}
	return false
}

// completeFromArgs answers the launch prompt the way an agent ends a phase, for the
// backends that take the prompt as a command-line argument.
func completeFromArgs(t *testing.T, sess *teardownSession) terminalStarter {
	t.Helper()
	return func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
		writeResult(t, resultPathFromPrompt(t, args))
		return sess, nil
	}
}

func runClaudeTeardown(t *testing.T, sess *teardownSession) (Result, error) {
	t.Helper()
	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		TrustPromptWait: time.Millisecond,
		endGrace:        teardownTestGrace,
		start:           completeFromArgs(t, sess),
	}
	return c.Run(context.Background(), "do the thing", "build")
}

func runCodexTeardown(t *testing.T, sess *teardownSession) (Result, error) {
	t.Helper()
	c := &CodexInteractive{
		Bin:             "codex",
		ResultDir:       t.TempDir(),
		SessionsDir:     t.TempDir(),
		TrustPromptWait: time.Millisecond,
		usageWait:       time.Millisecond,
		endGrace:        teardownTestGrace,
		start:           completeFromArgs(t, sess),
	}
	return c.Run(context.Background(), "do the thing", "build")
}

func runKimiTeardown(t *testing.T, sess *teardownSession) (Result, error) {
	t.Helper()
	var once sync.Once
	sess.onWrite = func(typed string) {
		once.Do(func() { writeResult(t, resultPathFromPaste(t, typed)) })
	}
	c := &KimiInteractive{
		Bin:          "kimi",
		ResultDir:    t.TempDir(),
		SessionsDir:  filepath.Join(t.TempDir(), "sessions"),
		ComposerWait: time.Millisecond,
		settle:       time.Millisecond,
		usageWait:    time.Millisecond,
		endGrace:     teardownTestGrace,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
		},
	}
	return c.Run(context.Background(), "do the thing", "build")
}

// TestPhaseEndTeardownClosesBeforeKill guards the stale index.lock: when the result
// file appears, the terminal is closed first so a git child mid-write gets the hangup
// it cleans up after, and the kill is only the backstop for a child still running when
// the grace window is out.
func TestPhaseEndTeardownClosesBeforeKill(t *testing.T) {
	backends := []struct {
		name string
		run  func(*testing.T, *teardownSession) (Result, error)
	}{
		{"claude", runClaudeTeardown},
		{"codex", runCodexTeardown},
		{"kimi", runKimiTeardown},
	}

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Run("exits on the hangup", func(t *testing.T) {
				sess := newTeardownSession()
				defer sess.stop()

				res, err := b.run(t, sess)
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if res.Final != "done" {
					t.Fatalf("Final = %q, want the run to have ended on the result file", res.Final)
				}
				if got := sess.order(); len(got) == 0 || got[0] != "close" {
					t.Fatalf("teardown order = %v, want the terminal closed first", got)
				}
				if sess.killed() {
					t.Errorf("teardown order = %v, want no kill for a session that exited on the hangup", sess.order())
				}
			})

			t.Run("hangs past the grace", func(t *testing.T) {
				sess := newTeardownSession()
				sess.hangs = true
				defer sess.stop()

				res, err := b.run(t, sess)
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if res.Final != "done" {
					t.Fatalf("Final = %q, want the run to have ended on the result file", res.Final)
				}
				want := []string{"close", "kill"}
				got := sess.order()
				if len(got) < len(want) || got[0] != want[0] || got[1] != want[1] {
					t.Errorf("teardown order = %v, want %v — the kill is the backstop, not the first move", got, want)
				}
			})
		})
	}
}
