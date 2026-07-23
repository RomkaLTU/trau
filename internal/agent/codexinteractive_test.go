package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
)

// TestCodexInteractiveArgsDriveTheTUI pins the shape that separates the
// interactive backend from the exec one: no `exec` subcommand, the dials passed
// as CLI flags, and the prompt as the trailing launch argument the TUI submits
// on its own.
func TestCodexInteractiveArgsDriveTheTUI(t *testing.T) {
	c := &CodexInteractive{
		Bin:     "codex",
		Flags:   []string{"--dangerously-bypass-approvals-and-sandbox"},
		Profile: "trau",
		Model:   "gpt-5.6-sol",
		Effort:  "medium",
	}
	args := c.args("do the thing")

	if args[0] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Errorf("args[0] = %q, want the configured flags first (no exec subcommand)", args[0])
	}
	for _, a := range args {
		if a == "exec" {
			t.Fatalf("interactive args must not run the exec subcommand: %v", args)
		}
	}
	if got := flagValue(args, "--model"); got != "gpt-5.6-sol" {
		t.Errorf("--model = %q, want gpt-5.6-sol", got)
	}
	if got := flagValue(args, "--profile"); got != "trau" {
		t.Errorf("--profile = %q, want trau", got)
	}
	if got := flagValue(args, "-c"); got != "model_reasoning_effort=medium" {
		t.Errorf("-c = %q, want model_reasoning_effort=medium", got)
	}
	if last := args[len(args)-1]; last != "do the thing" {
		t.Errorf("prompt is not the final arg: %q", last)
	}

	bare := (&CodexInteractive{Bin: "codex"}).args("p")
	if flagValue(bare, "--model") != "" || flagValue(bare, "-c") != "" || flagValue(bare, "--profile") != "" {
		t.Errorf("bare codex args should carry no dials: %v", bare)
	}
}

// TestCodexInteractiveCompletesOnResultFile pins the completion protocol: the
// run ends when the agent writes the result file it was told to write, and the
// file's contents — never stdout — are the final message.
func TestCodexInteractiveCompletesOnResultFile(t *testing.T) {
	sess := newScriptedSession("codex is working…\n")
	defer sess.stop()

	c := &CodexInteractive{
		Bin:             "codex",
		Model:           "gpt-5.6-sol",
		ResultDir:       t.TempDir(),
		SessionsDir:     t.TempDir(),
		TrustPromptWait: time.Millisecond,
		start: func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
			if err := os.WriteFile(resultPathFromPrompt(t, args), []byte("done"), 0o644); err != nil {
				return nil, err
			}
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
	if res.Model != "gpt-5.6-sol" {
		t.Errorf("Model = %q, want the configured model when no rollout is found", res.Model)
	}
}

// TestCodexInteractiveWaitsForRolloutUsage guards the accounting race: codex only
// records the turn's token_count once the tool call that wrote the result file has
// been fed back to the model, so a run that kills the session the instant the file
// appears recovers nothing at all. The session must stay open until the totals land.
func TestCodexInteractiveWaitsForRolloutUsage(t *testing.T) {
	sess := newScriptedSession("codex is working…\n")
	defer sess.stop()

	repo, sessions := t.TempDir(), t.TempDir()
	day := filepath.Join(sessions, "2026", "07", "23")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}

	full := rolloutFixture(repo)
	header := strings.Join(strings.Split(full, "\n")[:3], "\n") + "\n"
	rollout := filepath.Join(day, "rollout-2026-07-23T02-13-30-019f8c1b.jsonl")

	ledger := &recordingSink{}
	accounted := make(chan struct{})
	c := &CodexInteractive{
		Bin:             "codex",
		Dir:             repo,
		Model:           "gpt-5.6-sol",
		ResultDir:       t.TempDir(),
		SessionsDir:     sessions,
		TrustPromptWait: time.Millisecond,
		usageWait:       5 * time.Second,
		Tokens:          ledger,
		start: func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
			if err := os.WriteFile(rollout, []byte(header), 0o644); err != nil {
				return nil, err
			}
			if err := os.WriteFile(resultPathFromPrompt(t, args), []byte("done"), 0o644); err != nil {
				return nil, err
			}
			go func() {
				defer close(accounted)
				time.Sleep(300 * time.Millisecond)
				if err := os.WriteFile(rollout, []byte(full), 0o644); err != nil {
					t.Errorf("write rollout: %v", err)
				}
			}()
			return sess, nil
		},
	}

	res, err := c.Run(context.Background(), "do the thing", "build")
	<-accounted
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := Usage{Input: 200, Output: 40, CacheRead: 700, Reasoning: 20}
	if res.Usage != want {
		t.Errorf("Usage = %+v, want the rollout's totals %+v", res.Usage, want)
	}
	if res.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want 3", res.NumTurns)
	}
	if len(ledger.records) != 1 {
		t.Fatalf("ledger holds %d rows, want the one recovered call", len(ledger.records))
	}
	if got := ledger.records[0]; got.Input != want.Input || got.CacheRead != want.CacheRead {
		t.Errorf("ledger row = %+v, want it to carry the recovered usage", got)
	}
}

// TestCodexInteractiveUnrecoveredUsageStaysOutOfTheLedger pins what an
// unrecoverable call records: the agent_call event says the usage was never
// recovered, and nothing lands in the token ledger — a zero row there is
// indistinguishable from a real, free call.
func TestCodexInteractiveUnrecoveredUsageStaysOutOfTheLedger(t *testing.T) {
	sess := newScriptedSession("codex is working…\n")
	defer sess.stop()

	var events bytes.Buffer
	ledger := &recordingSink{}
	c := &CodexInteractive{
		Bin:             "codex",
		Model:           "gpt-5.6-sol",
		ResultDir:       t.TempDir(),
		SessionsDir:     t.TempDir(),
		TrustPromptWait: time.Millisecond,
		usageWait:       time.Millisecond,
		Log:             event.New(&events),
		Tokens:          ledger,
		start: func(_ context.Context, _, _ string, args []string, _, _ int) (terminalSession, error) {
			if err := os.WriteFile(resultPathFromPrompt(t, args), []byte("done"), 0o644); err != nil {
				return nil, err
			}
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

// TestCodexInteractiveAuthWallPauses is the codex half of COD-596: the sign-in
// screen keeps redrawing, so the stall watchdog never sees the run go quiet and it
// would otherwise burn the whole AGENT_TIMEOUT. The wall must be recognized in the
// terminal output and fail fast with ErrAuthRequired so the pipeline pauses
// blamelessly.
func TestCodexInteractiveAuthWallPauses(t *testing.T) {
	sess := newScriptedSession(codexSignInScreen)
	c := &CodexInteractive{
		Bin:             "codex",
		ResultDir:       t.TempDir(),
		SessionsDir:     t.TempDir(),
		StallWindow:     time.Hour,
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

// codexSignInScreen is codex's onboarding auth wall as the PTY delivers it, with
// the cursor repositioned before every word.
const codexSignInScreen = "\x1b[1;1H\x1b[1mSign\x1b[1;6Hin\x1b[1;9Hwith\x1b[1;14HChatGPT\x1b[22m" +
	"\x1b[3;3H›\x1b[3;5H1.\x1b[3;8HSign\x1b[3;13Hin\x1b[3;16Hwith\x1b[3;21HChatGPT" +
	"\x1b[4;5H2.\x1b[4;8HSign\x1b[4;13Hin\x1b[4;16Hfrom\x1b[4;21Hanother\x1b[4;29Hdevice" +
	"\x1b[5;5H3.\x1b[5;8HProvide\x1b[5;16Hyour\x1b[5;21Hown\x1b[5;25HAPI\x1b[5;29Hkey" +
	"\x1b[7;3HPress\x1b[7;9Henter\x1b[7;15Hto\x1b[7;18Hcontinue\n"

// TestHasCodexAuthWall covers the three walls a codex phase can hit — the
// onboarding screen, a refresh that needs a fresh login, and the generic provider
// failures — without pausing on prose that merely mentions signing in.
func TestHasCodexAuthWall(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"onboarding screen", codexSignInScreen, true},
		{"refresh token expired", "Your access token could not be refreshed because your refresh token has expired. Please log out and sign in again.", true},
		{"account switched", "Your access token could not be refreshed. Please sign in again.", true},
		{"generic provider wall", "\x1b[38;2;215;119;87mAPI Error: 403 \x1b[39mRequest not allowed", true},
		{"working agent", "\x1b[2;1H• Working (12s • esc to interrupt)", false},
		{"prose about signing in", "the README tells users to sign in with ChatGPT first", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCodexAuthWall(tc.in); got != tc.want {
				t.Errorf("hasCodexAuthWall(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// A queued note is typed into the live codex session as a bracketed paste and
// acked under the phase that took it, exactly as the claude backend does.
func TestCodexInteractiveSteerNoteIsPastedAndAcked(t *testing.T) {
	sess := newSteeredSession()
	defer sess.stop()

	var resultPath string
	c := &CodexInteractive{
		Bin:             "codex",
		ResultDir:       t.TempDir(),
		SessionsDir:     t.TempDir(),
		TrustPromptWait: time.Millisecond,
		steerPoll:       time.Millisecond,
		start:           finishOnResultPath(t, sess, &resultPath),
	}
	sess.onWrite = func() { writeResult(t, resultPath) }

	var (
		mu    sync.Mutex
		acks  []steerAck
		body  = "the staging DB is seeded now\npoint the test at it"
		notes = []SteerNote{{ID: 11, Body: body}}
	)
	src := SteerSource{
		Ticket:  "COD-9",
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
	if acks[0] != (steerAck{id: 11, phase: "build"}) {
		t.Errorf("ack = %+v, want note 11 acked under build", acks[0])
	}
}

// TestCodexSpecSelectsMode pins the fallback knob: the default builds the
// steerable interactive backend, and only an explicit exec mode falls back to
// print mode.
func TestCodexSpecSelectsMode(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"", "*agent.CodexInteractive"},
		{"interactive", "*agent.CodexInteractive"},
		{"exec", "*agent.Codex"},
	}
	for _, tc := range cases {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			r, err := codexSpec.New(BackendParams{Bin: "codex", Extra: map[string]string{"mode": tc.mode}})
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%T", r); got != tc.want {
				t.Errorf("backend = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestHasCodexTrustPrompt covers the reason codex needs its own matcher: its TUI
// positions the cursor before every word, so the dialog's sentence never appears
// contiguously in the byte stream the drain sees.
func TestHasCodexTrustPrompt(t *testing.T) {
	dialog := "\x1b[1;1H>\x1b[1;3H\x1b[1mYou are in \x1b[22m/repo" +
		"\x1b[3;3HDo\x1b[3;6Hyou\x1b[3;10Htrust\x1b[3;16Hthe\x1b[3;20Hcontents\x1b[3;29Hof" +
		"\x1b[3;32Hthis\x1b[3;37Hdirectory?\x1b[6;1H\x1b[38;5;6;49m› 1. Yes, continue" +
		"\x1b[7;3H2. No, quit"

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"word-positioned dialog", dialog, true},
		{"plain wording", "Do you trust the contents of this directory? 1. Yes, continue", true},
		{"working agent", "\x1b[2;1H• Working (12s • esc to interrupt)", false},
		{"prose about trust", "the linter does not trust this config", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasCodexTrustPrompt(tc.in); got != tc.want {
				t.Errorf("hasCodexTrustPrompt(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
