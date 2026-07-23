package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/tokens"
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

// TestClaudeArgsStripMechanicalMCP pins the COD-801 flag gating: with stripping on,
// only mechanical phases get --strict-mcp-config (build/handoff/verify keep their MCP
// config); with the opt-out off, no phase gets the flag.
func TestClaudeArgsStripMechanicalMCP(t *testing.T) {
	has := func(args []string) bool {
		for _, a := range args {
			if a == "--strict-mcp-config" {
				return true
			}
		}
		return false
	}

	on := &ClaudeInteractive{Bin: "claude", StripMechanicalMCP: true}
	for _, label := range []string{"cleanup", "commit", "repair1", "bugfix2", "push-repair1"} {
		if !has(on.args("prompt", "sid", label)) {
			t.Errorf("StripMechanicalMCP on: %q should pass --strict-mcp-config", label)
		}
	}
	for _, label := range []string{"build", "handoff", "verify", "pick"} {
		if has(on.args("prompt", "sid", label)) {
			t.Errorf("StripMechanicalMCP on: %q must keep its MCP config", label)
		}
	}

	off := &ClaudeInteractive{Bin: "claude", StripMechanicalMCP: false}
	if has(off.args("prompt", "sid", "cleanup")) {
		t.Error("opt-out off: cleanup must not pass --strict-mcp-config")
	}
}

// TestClaudeSessionHookMatchesSessionIDArg pins the takeover handle (ADR 0018):
// OnSessionStart fires with the same uuid the args builder passes as
// --session-id, and it fires before the terminal session spawns, so the id is
// durable before the session can produce its first byte.
func TestClaudeSessionHookMatchesSessionIDArg(t *testing.T) {
	spawnRefused := errors.New("spawn refused")
	var hooked, hookedLabel string
	var spawnArgs []string
	c := &ClaudeInteractive{
		Bin:       "claude",
		ResultDir: t.TempDir(),
		OnSessionStart: func(sessionID, label string) {
			hooked, hookedLabel = sessionID, label
		},
		start: func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
			if hooked == "" {
				t.Error("terminal session spawned before OnSessionStart fired")
			}
			spawnArgs = args
			return nil, spawnRefused
		},
	}

	if _, err := c.Run(context.Background(), "prompt", "build"); !errors.Is(err, spawnRefused) {
		t.Fatalf("err = %v, want the spawn error", err)
	}
	if hookedLabel != "build" {
		t.Errorf("hook label = %q, want build", hookedLabel)
	}
	sid := ""
	for i, a := range spawnArgs {
		if a == "--session-id" && i+1 < len(spawnArgs) {
			sid = spawnArgs[i+1]
		}
	}
	if sid == "" || sid != hooked {
		t.Errorf("--session-id = %q, hook got %q — want the same non-empty uuid", sid, hooked)
	}
}

// TestCodexIgnoresSessionHook pins that only claude wires OnSessionStart: a
// codex backend built from the same BackendParams never fires it.
func TestCodexIgnoresSessionHook(t *testing.T) {
	fired := false
	r, err := codexSpec.New(BackendParams{
		Bin:            "trau-test-no-such-codex",
		OnSessionStart: func(string, string) { fired = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), "prompt", "build"); err == nil {
		t.Fatal("Run with a missing binary should error")
	}
	if fired {
		t.Error("codex backend fired OnSessionStart; only claude mints session ids")
	}
}

// TestCodexArgsPassModelAndEffort checks the clean codex default (gpt-5.6-sol at
// medium effort) reaches a fresh `codex exec` as an explicit --model plus a
// -c model_reasoning_effort override, and that an unset dial emits no flag at all.
func TestCodexArgsPassModelAndEffort(t *testing.T) {
	c := &Codex{Bin: "codex", Model: "gpt-5.6-sol", Effort: "medium"}
	args := c.args("do the thing", "/tmp/msg.json")

	if args[0] != "exec" {
		t.Fatalf("args[0] = %q, want exec", args[0])
	}
	if got := flagValue(args, "--model"); got != "gpt-5.6-sol" {
		t.Errorf("--model = %q, want gpt-5.6-sol", got)
	}
	if got := flagValue(args, "-c"); got != "model_reasoning_effort=medium" {
		t.Errorf("-c = %q, want model_reasoning_effort=medium", got)
	}
	if last := args[len(args)-1]; last != "do the thing" {
		t.Errorf("prompt is not the final arg: %q", last)
	}

	bare := (&Codex{Bin: "codex"}).args("p", "/tmp/m")
	if flagValue(bare, "--model") != "" || flagValue(bare, "-c") != "" {
		t.Errorf("bare codex args should carry no model/effort flags: %v", bare)
	}
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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

// TestClaudeInteractiveAuthFailurePauses is the COD-596 guard: when an agent exits
// after rendering an auth/login wall without a result, the confirmed auth signal
// must win over the generic session-exit error so the pipeline pauses blamelessly.
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
	sess.stop()

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

func TestClaudeInteractiveAuthQuoteWithResultSucceeds(t *testing.T) {
	sess := newScriptedSession(`Reviewing the "please run /login" matcher in prose.` + "\n")
	defer sess.stop()

	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		TrustPromptWait: time.Millisecond,
		start: func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
			if err := os.WriteFile(resultPathFromPrompt(t, args), []byte("done"), 0o644); err != nil {
				return nil, err
			}
			return sess, nil
		},
	}

	res, err := c.Run(context.Background(), "do the thing", "verify")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IsError {
		t.Fatal("res.IsError = true, want a successful result")
	}
	if res.Final != "done" {
		t.Fatalf("res.Final = %q, want done", res.Final)
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

type fakeTranscriptSink struct {
	stem string
	cols int
	rows int
	buf  bytes.Buffer
}

func (f *fakeTranscriptSink) Open(stem string, cols, rows int) io.WriteCloser {
	f.stem, f.cols, f.rows = stem, cols, rows
	return nopWriteCloser{&f.buf}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// TestLiveTranscriptTeesAndAnnounces guards the non-PTY backends (codex/kimi):
// liveTranscript must open a session writer on the sink keyed by the stem, tee the
// agent's output to it, and emit the agent_start event carrying the transcript id
// the TUI live view follows — the contract that lets all three providers share one
// viewer with no provider branching (ADR 0008 §4).
func TestLiveTranscriptTeesAndAnnounces(t *testing.T) {
	var events bytes.Buffer
	now := time.Unix(0, 1234567890)
	sink := &fakeTranscriptSink{}

	w, ok := liveTranscript(sink, event.New(&events), "build", 100, 40, now)
	if !ok || w == nil {
		t.Fatalf("liveTranscript ok=%v writer!=nil=%v, want a live writer", ok, w != nil)
	}
	defer func() { _ = w.Close() }()

	if sink.stem != "1234567890-build" {
		t.Errorf("session stem = %q, want 1234567890-build", sink.stem)
	}
	if sink.cols != 100 || sink.rows != 40 {
		t.Errorf("session dims = %dx%d, want 100x40", sink.cols, sink.rows)
	}
	if _, err := io.WriteString(w, "hello agent\n"); err != nil {
		t.Fatalf("write live transcript: %v", err)
	}
	if !strings.Contains(sink.buf.String(), "hello agent") {
		t.Errorf("agent output not teed to the sink: %q", sink.buf.String())
	}
	if s := events.String(); !strings.Contains(s, event.KindAgentStart) || !strings.Contains(s, "1234567890-build") {
		t.Errorf("agent_start event must carry the transcript id; got: %s", s)
	}

	if _, ok := liveTranscript(nil, event.New(&events), "build", 100, 40, now); ok {
		t.Error("a nil sink must disable capture")
	}
}

// recordingSink captures the token records a backend appends, so the ledger's
// per-call fields can be asserted without a hub.
type recordingSink struct {
	phases  []string
	records []tokens.Record
}

func (r *recordingSink) Append(phase string, rec tokens.Record) {
	r.phases = append(r.phases, phase)
	r.records = append(r.records, rec)
}

// resultPathFromPrompt recovers the result path the backend told the agent to
// write, so a fake session can complete the run the way the real CLI does.
func resultPathFromPrompt(t *testing.T, args []string) string {
	t.Helper()
	const marker = "creating parent directories if needed: "
	prompt := args[len(args)-1]
	i := strings.Index(prompt, marker)
	if i < 0 {
		t.Fatalf("prompt carries no result path: %q", prompt)
	}
	path, _, _ := strings.Cut(prompt[i+len(marker):], "\n")
	return path
}

// TestClaudeInteractiveRecordsEffortAndDuration covers the ledger's routing
// fields: a completed call reports the effort it was launched with and its
// measured wall-clock duration, not just its token counts.
func TestClaudeInteractiveRecordsEffortAndDuration(t *testing.T) {
	sink := &recordingSink{}
	sess := newScriptedSession("working…\n")
	defer sess.stop()

	base := time.Unix(1_700_000_000, 0)
	var calls int64
	c := &ClaudeInteractive{
		Bin:             "claude",
		Model:           "opus",
		Effort:          "xhigh",
		ResultDir:       t.TempDir(),
		TrustPromptWait: time.Millisecond,
		Tokens:          sink,
		now: func() time.Time {
			n := atomic.AddInt64(&calls, 1)
			return base.Add(time.Duration(n) * time.Second)
		},
		start: func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
			if err := os.WriteFile(resultPathFromPrompt(t, args), []byte("done"), 0o644); err != nil {
				return nil, err
			}
			return sess, nil
		},
	}

	if _, err := c.Run(context.Background(), "do the thing", "verify"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sink.records) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(sink.records))
	}
	if sink.phases[0] != "verify" {
		t.Errorf("phase = %q, want verify", sink.phases[0])
	}
	rec := sink.records[0]
	if rec.Effort != "xhigh" {
		t.Errorf("effort = %q, want xhigh", rec.Effort)
	}
	if rec.Duration <= 0 {
		t.Errorf("duration = %v, want the measured wall clock", rec.Duration)
	}
	if rec.Model != "opus" || rec.Provider != "claude" {
		t.Errorf("record = %+v, want the claude/opus route it ran under", rec)
	}
}
