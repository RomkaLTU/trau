package agent

import (
	"context"
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
		start: func(context.Context, string, string, []string) (terminalSession, error) {
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
