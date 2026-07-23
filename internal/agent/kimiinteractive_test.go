package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
)

// kimiComposerSession is a live kimi terminal: its first read delivers the startup
// output, then it stays open recording every keystroke typed into it. onPaste fires
// with the running transcript so a test can answer the prompt the way the real
// agent does.
type kimiComposerSession struct {
	mu        sync.Mutex
	typed     []byte
	out       []byte
	sent      bool
	onPaste   func(typed string)
	announced chan struct{}
	done      chan struct{}
	once      sync.Once
}

// kimiComposerReady is the sequence kimi emits when its composer starts reading
// input: bracketed paste on.
const kimiComposerReady = "\x1b[?2004h"

// newKimiComposerSession is a kimi terminal that has already drawn out by the time
// the backend reads it.
func newKimiComposerSession(out string) *kimiComposerSession {
	s := newQuietKimiSession(out)
	s.announce()
	return s
}

// newQuietKimiSession withholds its startup output until announce, so a test can
// watch what the backend does while the composer is still coming up.
func newQuietKimiSession(out string) *kimiComposerSession {
	return &kimiComposerSession{
		out:       []byte(out),
		announced: make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (s *kimiComposerSession) announce() { close(s.announced) }

func (s *kimiComposerSession) Read(p []byte) (int, error) {
	if !s.sent {
		s.sent = true
		select {
		case <-s.announced:
			return copy(p, s.out), nil
		case <-s.done:
			return 0, io.EOF
		}
	}
	<-s.done
	return 0, io.EOF
}

func (s *kimiComposerSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.typed = append(s.typed, p...)
	typed, onPaste := string(s.typed), s.onPaste
	s.mu.Unlock()
	if onPaste != nil {
		onPaste(typed)
	}
	return len(p), nil
}

func (s *kimiComposerSession) Wait() error  { <-s.done; return nil }
func (s *kimiComposerSession) stop()        { s.once.Do(func() { close(s.done) }) }
func (s *kimiComposerSession) Close() error { s.stop(); return nil }
func (s *kimiComposerSession) Kill() error  { s.stop(); return nil }

func (s *kimiComposerSession) transcript() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.typed)
}

// resultPathFromPaste recovers the result path out of the prompt the backend typed
// into the composer — kimi takes no prompt argument to read it out of.
func resultPathFromPaste(t *testing.T, typed string) string {
	t.Helper()
	const marker = "creating parent directories if needed: "
	i := strings.Index(typed, marker)
	if i < 0 {
		t.Fatalf("nothing typed carries a result path: %q", typed)
	}
	path, _, _ := strings.Cut(typed[i+len(marker):], "\n")
	return path
}

// completeOnPrompt answers the launch prompt by writing the result file, the way
// the real agent ends a phase.
func completeOnPrompt(t *testing.T, sess *kimiComposerSession) {
	t.Helper()
	var once sync.Once
	sess.onPaste = func(typed string) {
		once.Do(func() { writeResult(t, resultPathFromPaste(t, typed)) })
	}
}

// TestKimiInteractiveArgsDriveTheTUI pins the shape that separates the interactive
// backend from the print one: no -p and no stream-json, tool approval turned off,
// and no prompt on the command line at all — kimi accepts none, so it is typed in.
func TestKimiInteractiveArgsDriveTheTUI(t *testing.T) {
	c := &KimiInteractive{Bin: "kimi", Flags: []string{"--plan"}, Model: "kimi-for-coding"}
	args := c.args()

	if args[0] != "--plan" {
		t.Errorf("args[0] = %q, want the configured flags first", args[0])
	}
	for _, a := range args {
		if a == "-p" || a == "--prompt" || a == "--output-format" {
			t.Fatalf("interactive args must not run print mode: %v", args)
		}
	}
	if got := flagValue(args, "--model"); got != "kimi-for-coding" {
		t.Errorf("--model = %q, want kimi-for-coding", got)
	}
	if !slicesContains(args, "--yolo") {
		t.Errorf("args = %v, want --yolo so tool calls never block on an approval dialog", args)
	}

	bare := (&KimiInteractive{Bin: "kimi"}).args()
	if flagValue(bare, "--model") != "" {
		t.Errorf("bare kimi args should carry no model flag: %v", bare)
	}
}

func slicesContains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestKimiInteractivePastesPromptAfterComposerReady pins the launch contract kimi
// alone needs: the prompt is a bracketed paste typed into the composer, and it is
// held back until the TUI has turned bracketed paste on — sent earlier, the markers
// read as stray escape keys and a multi-line prompt arrives mangled.
func TestKimiInteractivePastesPromptAfterComposerReady(t *testing.T) {
	sess := newQuietKimiSession(kimiComposerReady)
	defer sess.stop()

	pasted := make(chan string, 1)
	sess.onPaste = func(typed string) {
		select {
		case pasted <- typed:
		default:
		}
	}

	c := &KimiInteractive{
		Bin:          "kimi",
		Preamble:     "preamble",
		ResultDir:    t.TempDir(),
		ComposerWait: time.Hour,
		settle:       time.Millisecond,
		usageWait:    time.Millisecond,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
		},
	}

	go func() { _, _ = c.Run(context.Background(), "do the thing", "build") }()

	select {
	case <-pasted:
		t.Fatal("the prompt was typed before the composer announced itself")
	case <-time.After(150 * time.Millisecond):
	}

	sess.announce()

	select {
	case typed := <-pasted:
		if !strings.HasPrefix(typed, "\x1b[200~") || !strings.HasSuffix(typed, "\x1b[201~\r") {
			t.Errorf("prompt was typed as %q, want a bracketed paste", typed)
		}
		if !strings.Contains(typed, "preamble") || !strings.Contains(typed, "do the thing") {
			t.Errorf("prompt %q is missing the preamble or the caller's prompt", typed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt was never typed after the composer announced itself")
	}
}

// TestKimiInteractiveCompletesOnResultFile pins the completion protocol: the run
// ends when the agent writes the result file the typed prompt named, and the file's
// contents — never stdout — are the final message.
func TestKimiInteractiveCompletesOnResultFile(t *testing.T) {
	sess := newKimiComposerSession(kimiComposerReady)
	defer sess.stop()
	completeOnPrompt(t, sess)

	c := &KimiInteractive{
		Bin:          "kimi",
		Model:        "kimi-for-coding",
		ResultDir:    t.TempDir(),
		SessionsDir:  filepath.Join(t.TempDir(), "sessions"),
		ComposerWait: time.Millisecond,
		settle:       time.Millisecond,
		usageWait:    time.Millisecond,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
		},
	}

	res, err := c.Run(context.Background(), "do the thing", "build")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Final != "done" {
		t.Errorf("Final = %q, want the result file's contents", res.Final)
	}
	if res.Model != "kimi-for-coding" {
		t.Errorf("Model = %q, want the configured model when no session is found", res.Model)
	}
}

// TestKimiInteractiveWaitsForSessionUsage guards the accounting race: kimi records
// a turn's usage only once the turn ends, and the turn that wrote the result file is
// still running when the file appears — so a run that kills the session the instant
// it lands recovers nothing. The session must stay open until the totals do.
func TestKimiInteractiveWaitsForSessionUsage(t *testing.T) {
	sess := newKimiComposerSession(kimiComposerReady)
	defer sess.stop()

	home, repo := t.TempDir(), t.TempDir()
	accounted := make(chan struct{})
	completeOnPrompt(t, sess)

	ledger := &recordingSink{}
	c := &KimiInteractive{
		Bin:          "kimi",
		Dir:          repo,
		Model:        "kimi-for-coding",
		ResultDir:    t.TempDir(),
		SessionsDir:  filepath.Join(home, "sessions"),
		ComposerWait: time.Millisecond,
		settle:       time.Millisecond,
		usageWait:    5 * time.Second,
		Tokens:       ledger,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			// Kimi indexes the session and opens its wire.jsonl as it starts, but
			// only closes out the running turn's accounting later.
			dir := kimiSessionFixture(t, home, "session_live", repo, []string{kimiUsageLine(1200, 300, 4000, 500)})
			go func() {
				defer close(accounted)
				time.Sleep(300 * time.Millisecond)
				writeWire(t, dir, []string{
					kimiUsageLine(1200, 300, 4000, 500),
					kimiUsageLine(800, 150, 6000, 0),
				})
			}()
			return sess, nil
		},
	}

	res, err := c.Run(context.Background(), "do the thing", "build")
	<-accounted
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := Usage{Input: 2000, Output: 450, CacheRead: 10000, CacheCreation: 500}
	if res.Usage != want {
		t.Errorf("Usage = %+v, want the session's flushed totals %+v", res.Usage, want)
	}
	if res.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", res.NumTurns)
	}
	if res.Model != "kimi-code/kimi-for-coding" {
		t.Errorf("Model = %q, want the model the session's turns ran under", res.Model)
	}
	if len(ledger.records) != 1 {
		t.Fatalf("ledger holds %d rows, want the one recovered call", len(ledger.records))
	}
	got := ledger.records[0]
	if got.Input != want.Input || got.CacheRead != want.CacheRead {
		t.Errorf("ledger row = %+v, want it to carry the recovered usage", got)
	}
	if got.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil — kimi bills a subscription, not per call", *got.CostUSD)
	}
}

func writeWire(t *testing.T, sessionDir string, lines []string) {
	t.Helper()
	path := filepath.Join(sessionDir, "agents", "main", "wire.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Errorf("write wire.jsonl: %v", err)
	}
}

// TestKimiInteractiveUnrecoveredUsageStaysOutOfTheLedger pins what an
// unrecoverable call records: the agent_call event says the usage was never
// recovered, and nothing lands in the token ledger — a zero row there is
// indistinguishable from a real, free call.
func TestKimiInteractiveUnrecoveredUsageStaysOutOfTheLedger(t *testing.T) {
	sess := newKimiComposerSession(kimiComposerReady)
	defer sess.stop()
	completeOnPrompt(t, sess)

	var events bytes.Buffer
	ledger := &recordingSink{}
	c := &KimiInteractive{
		Bin:          "kimi",
		Dir:          t.TempDir(),
		Model:        "kimi-for-coding",
		ResultDir:    t.TempDir(),
		SessionsDir:  filepath.Join(t.TempDir(), "sessions"),
		ComposerWait: time.Millisecond,
		settle:       time.Millisecond,
		usageWait:    time.Millisecond,
		Log:          event.New(&events),
		Tokens:       ledger,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
		},
	}

	if _, err := c.Run(context.Background(), "do the thing", "build"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ledger.records) != 0 {
		t.Errorf("ledger holds %+v, want no row for a call with no accounting", ledger.records)
	}
	if !strings.Contains(events.String(), `"usage_recovered":false`) {
		t.Errorf("agent_call must report the usage unrecovered; got: %s", events.String())
	}
}

// TestKimiInteractiveAuthWallPauses is the kimi half of COD-596: the error sits
// under a composer that keeps animating, so the stall watchdog never sees the run go
// quiet and it would otherwise burn the whole AGENT_TIMEOUT. The wall must be
// recognized in the terminal output and fail fast with ErrAuthRequired so the
// pipeline pauses blamelessly.
func TestKimiInteractiveAuthWallPauses(t *testing.T) {
	sess := newKimiComposerSession(kimiMembershipWall)
	c := &KimiInteractive{
		Bin:          "kimi",
		ResultDir:    t.TempDir(),
		SessionsDir:  filepath.Join(t.TempDir(), "sessions"),
		StallWindow:  time.Hour,
		ComposerWait: time.Millisecond,
		settle:       time.Millisecond,
		usageWait:    time.Millisecond,
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

// kimiMembershipWall is the entitlement wall kimi draws when it cannot verify the
// account's subscription, wrapped across the pane the way the PTY delivers it.
const kimiMembershipWall = "\x1b[2K   \x1b[38;2;232;84;84mError: [provider.api_error] 402 We're unable to verify your " +
	"membership benefits at this time. Please ensure your\r\n\x1b[2K \x1b[38;2;232;84;84mmembership is active.\x1b[39m\r\n"

// TestHasKimiAuthWall covers the walls a kimi phase can hit — an unverifiable
// membership and a rejected provider call — without pausing on a working agent or on
// prose that merely mentions a membership.
func TestHasKimiAuthWall(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"membership wall", kimiMembershipWall, true},
		{"unauthorized provider call", "Error: [provider.api_error] 401 Unauthorized", true},
		{"generic provider wall", "\x1b[38;2;215;119;87mAPI Error: 403 \x1b[39mRequest not allowed", true},
		{"working agent", "\x1b[2;1H🌒 · Tip: /goal for multi-step work with a clear finish line", false},
		{"prose about membership", "the README explains how to verify your membership tier", false},
		{"unrelated 402 in prose", "the handler returns 402 when the plan lapsed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasKimiAuthWall(tc.in); got != tc.want {
				t.Errorf("hasKimiAuthWall(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHasKimiComposer covers the launch gate: the handshake kimi emits when it
// starts reading input, and not the ordinary drawing that precedes it.
func TestHasKimiComposer(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"bracketed paste on", "\x1b]11;?\x07\x1b[?2026h \x1b[0m\x1b[?25l" + kimiComposerReady + "\x1b[>7u", true},
		{"startup banner only", "Kimi Code updated to v0.22.3\r\nChangelog: https://example.invalid\r\n", false},
		{"cursor hidden", "\x1b[?25l\x1b[?2026h", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasKimiComposer(tc.in); got != tc.want {
				t.Errorf("hasKimiComposer(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// A queued note is typed into the live kimi session as a bracketed paste and acked
// under the phase that took it, exactly as the claude and codex backends do.
func TestKimiInteractiveSteerNoteIsPastedAndAcked(t *testing.T) {
	sess := newKimiComposerSession(kimiComposerReady)
	defer sess.stop()

	body := "the staging DB is seeded now\npoint the test at it"
	var once sync.Once
	sess.onPaste = func(typed string) {
		if strings.Contains(typed, body) {
			once.Do(func() { writeResult(t, resultPathFromPaste(t, typed)) })
		}
	}

	var (
		mu   sync.Mutex
		acks []steerAck
	)
	src := SteerSource{
		Ticket:  "COD-9",
		Pending: func(context.Context) ([]SteerNote, error) { return []SteerNote{{ID: 11, Body: body}}, nil },
		Ack: func(_ context.Context, id int64, phase string) error {
			mu.Lock()
			defer mu.Unlock()
			acks = append(acks, steerAck{id: id, phase: phase})
			return nil
		},
	}

	c := &KimiInteractive{
		Bin:          "kimi",
		ResultDir:    t.TempDir(),
		SessionsDir:  filepath.Join(t.TempDir(), "sessions"),
		ComposerWait: time.Millisecond,
		settle:       time.Millisecond,
		usageWait:    time.Millisecond,
		steerPoll:    time.Millisecond,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
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
	if acks[0] != (steerAck{id: 11, phase: "build"}) {
		t.Errorf("ack = %+v, want note 11 acked under build", acks[0])
	}
}

// TestKimiSpecSelectsMode pins the fallback knob: the default builds the steerable
// interactive backend, and only an explicit print mode falls back to `kimi -p`.
func TestKimiSpecSelectsMode(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"", "*agent.KimiInteractive"},
		{"interactive", "*agent.KimiInteractive"},
		{"print", "*agent.Kimi"},
	}
	for _, tc := range cases {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			r, err := kimiSpec.New(BackendParams{Bin: "kimi", Extra: map[string]string{"mode": tc.mode}})
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%T", r); got != tc.want {
				t.Errorf("backend = %s, want %s", got, tc.want)
			}
		})
	}
}
