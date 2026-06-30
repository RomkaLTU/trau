package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stallSession is a terminalSession wedged before it ever produces output and
// never exiting on its own — the COD-498 stall — until Kill/Close unblocks its
// readers. Run's stall watchdog is the only thing that can end it.
type stallSession struct {
	done chan struct{}
	once sync.Once
}

func newStallSession() *stallSession { return &stallSession{done: make(chan struct{})} }

func (s *stallSession) Read([]byte) (int, error)    { <-s.done; return 0, io.EOF }
func (s *stallSession) Write(p []byte) (int, error) { return len(p), nil }
func (s *stallSession) Wait() error                 { <-s.done; return nil }
func (s *stallSession) stop()                       { s.once.Do(func() { close(s.done) }) }
func (s *stallSession) Close() error                { s.stop(); return nil }
func (s *stallSession) Kill() error                 { s.stop(); return nil }

// TestClaudeInteractiveStallWindowKills is the COD-583 guard: an agent that emits
// no transcript output is killed and surfaces a stall error well before
// AGENT_TIMEOUT (here disabled), so the pipeline can recover instead of waiting the
// full timeout. A fake clock advances faster than the stall window so the watchdog
// trips deterministically without real waiting.
func TestClaudeInteractiveStallWindowKills(t *testing.T) {
	sess := newStallSession()
	base := time.Unix(1_700_000_000, 0)
	var calls int64
	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		Timeout:         0, // no hard timeout: only the stall watchdog can end this run
		StallWindow:     3 * time.Second,
		TrustPromptWait: time.Millisecond,
		now: func() time.Time {
			n := atomic.AddInt64(&calls, 1)
			return base.Add(time.Duration(n) * 5 * time.Second)
		},
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
		},
	}

	type outcome struct {
		res Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := c.Run(context.Background(), "do the thing", "handoff")
		ch <- outcome{res, err}
	}()

	select {
	case got := <-ch:
		if got.err == nil {
			t.Fatal("Run returned nil error, want a stall error")
		}
		if !strings.Contains(got.err.Error(), "stalled") {
			t.Fatalf("err = %v, want it to mention a stall", got.err)
		}
		if !got.res.IsError {
			t.Error("res.IsError = false, want true on a stall kill")
		}
	case <-time.After(5 * time.Second):
		sess.stop()
		t.Fatal("Run did not return within 5s — the stall watchdog never fired")
	}
}

// scriptedSession emits a fixed prologue on its first Read, then blocks like a
// hung agent (the auth-wall idle) until Kill/Close. It lets a test feed the
// terminal output that should trip the auth watchdog.
type scriptedSession struct {
	out  []byte
	sent bool
	done chan struct{}
	once sync.Once
}

func newScriptedSession(out string) *scriptedSession {
	return &scriptedSession{out: []byte(out), done: make(chan struct{})}
}

func (s *scriptedSession) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		return copy(p, s.out), nil
	}
	<-s.done
	return 0, io.EOF
}
func (s *scriptedSession) Write(p []byte) (int, error) { return len(p), nil }
func (s *scriptedSession) Wait() error                 { <-s.done; return nil }
func (s *scriptedSession) stop()                       { s.once.Do(func() { close(s.done) }) }
func (s *scriptedSession) Close() error                { s.stop(); return nil }
func (s *scriptedSession) Kill() error                 { s.stop(); return nil }

// TestClaudeInteractiveAuthFailurePauses is the COD-596 guard: when the agent hits
// a provider auth/login wall it idles producing no result, so trau must recognize
// the wall in the terminal output and fail fast with ErrAuthRequired — letting the
// pipeline pause blamelessly — instead of waiting out the (here, very long) stall
// window and then retrying into the same wall.
func TestClaudeInteractiveAuthFailurePauses(t *testing.T) {
	sess := newScriptedSession("⏺ API Error: 403 Request not allowed · Please run /login\n")
	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		Timeout:         0,         // no hard timeout
		StallWindow:     time.Hour, // only the auth watchdog should end this run
		TrustPromptWait: time.Millisecond,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
		},
	}

	type outcome struct {
		res Result
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := c.Run(context.Background(), "do the thing", "build")
		ch <- outcome{res, err}
	}()

	select {
	case got := <-ch:
		if !errors.Is(got.err, ErrAuthRequired) {
			t.Fatalf("err = %v, want it to wrap ErrAuthRequired", got.err)
		}
		if !got.res.IsError {
			t.Error("res.IsError = false, want true on an auth-wall kill")
		}
	case <-time.After(5 * time.Second):
		sess.stop()
		t.Fatal("Run did not return within 5s — the auth watchdog never fired")
	}
}

// TestHasAuthFailure pins the marker matcher: real provider auth walls (including
// ones drawn with interleaved ANSI styling) match, while a bare "403" or prose
// that merely contains "not allowed" does not — the 403 case needs both tokens so
// incidental output can't trigger a false pause.
func TestHasAuthFailure(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"login wall", "Please run /login", true},
		{"403 with ansi", "\x1b[38;2;215;119;87mAPI Error: 403 \x1b[39mRequest not allowed", true},
		{"invalid key", "API Error: Invalid API key · Please run /login", true},
		{"credit balance", "Credit balance is too low", true},
		{"oauth expired", "Your OAuth token has expired; please re-login", true},
		{"bare 403", "got 403 results back", false},
		{"prose not allowed", "that request was not allowed by the linter", false},
		{"working agent", "Running 1 shell command…", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAuthFailure(tc.in); got != tc.want {
				t.Errorf("hasAuthFailure(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
