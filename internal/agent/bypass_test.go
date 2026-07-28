package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const bypassDialog = `WARNING: Claude Code running in Bypass Permissions mode

In Bypass Permissions mode, Claude Code will not ask for your approval before running potentially dangerous commands.

By proceeding, you accept all responsibility for actions taken while running in Bypass Permissions mode.

❯ 1. No, exit
  2. Yes, I accept
`

func TestHasClaudeBypassPrompt(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"the real dialog", bypassDialog, true},
		{"styled with ANSI", "\x1b[1mWARNING: Claude Code running in Bypass Permissions mode\x1b[0m\n❯ 1. No, exit\n  2. Yes, I \x1b[1maccept\x1b[0m", true},
		{"prose naming the mode without the accept verb", "the Bypass Permissions mode docs describe the flag", false},
		{"prose accepting something else", "please accept the meeting invite", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasClaudeBypassPrompt(tc.text); got != tc.want {
				t.Errorf("hasClaudeBypassPrompt(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestHasInteractiveMenu(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"claude-style pointer on a numbered option", "❯ 1. No, exit", true},
		{"codex-style pointer", "› 2. Yes, continue", true},
		{"styled with ANSI", "\x1b[36m❯\x1b[0m 1. Yes, proceed", true},
		{"bare composer pointer", "❯ Try \"fix the failing test\"", false},
		{"numbered list without a pointer", "steps: 1. build 2. verify", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasInteractiveMenu(tc.text); got != tc.want {
				t.Errorf("hasInteractiveMenu(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestClaudeBypassAccepted(t *testing.T) {
	cases := []struct {
		name    string
		file    string // "" = no file written
		want    bool
		wantErr bool
	}{
		{"accepted", `{"bypassPermissionsModeAccepted":true}`, true, false},
		{"explicitly not accepted", `{"bypassPermissionsModeAccepted":false}`, false, false},
		{"key absent", `{"theme":"dark"}`, false, false},
		{"no config file", "", false, false},
		{"unparsable file", `{not json`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", dir)
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(tc.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ClaudeBypassAccepted()
			if got != tc.want {
				t.Errorf("accepted = %v, want %v", got, tc.want)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// A sighted dialog followed by process end must confirm through the debouncer's
// finish path — the child sat on the dialog until something killed it.
func TestDrainConfirmsBypassPromptOnEOF(t *testing.T) {
	var transcript bytes.Buffer
	bypassPrompt := make(chan struct{}, 1)
	drainWithSignals(&transcript, strings.NewReader(bypassDialog), claudeWatch, terminalSignals{bypass: bypassPrompt}, nil)
	select {
	case <-bypassPrompt:
	default:
		t.Fatal("bypass dialog on a stream that then ended was not confirmed")
	}
}

// Prose quoting the dialog (a ticket about this very dialog does) keeps
// scrolling, and the continued output must disarm the sighting: no signal.
func TestDrainDisarmsBypassPromptOnContinuedOutput(t *testing.T) {
	var transcript bytes.Buffer
	bypassPrompt := make(chan struct{}, 1)
	prose := bypassDialog + strings.Repeat("the build kept working after quoting it\n", 600)
	drainWithSignals(&transcript, strings.NewReader(prose), claudeWatch, terminalSignals{bypass: bypassPrompt}, nil)
	select {
	case <-bypassPrompt:
		t.Fatal("prose quoting the dialog must not confirm a bypass sighting")
	default:
	}
}

// Kimi shares the generic menu matcher: a blocking selection dialog followed by
// process end confirms on the menu channel.
func TestDrainConfirmsMenuPromptForKimi(t *testing.T) {
	var transcript bytes.Buffer
	menuPrompt := make(chan struct{}, 1)
	drainWithSignals(&transcript, strings.NewReader("Allow network access?\n❯ 1. Yes\n  2. No\n"), kimiWatch, terminalSignals{menu: menuPrompt}, nil)
	select {
	case <-menuPrompt:
	default:
		t.Fatal("kimi selection dialog on a stream that then ended was not confirmed")
	}
}

// The full claude run must fail fast with the specific sentinel when the child
// sits on the bypass dialog and then goes away — not stall into the watchdog.
func TestClaudeInteractiveFailsFastOnBypassPrompt(t *testing.T) {
	sess := newScriptedSession(bypassDialog)
	time.AfterFunc(100*time.Millisecond, sess.stop)

	c := &ClaudeInteractive{
		Bin:             "claude",
		ResultDir:       t.TempDir(),
		TrustPromptWait: time.Millisecond,
		start: func(context.Context, string, string, []string, int, int) (terminalSession, error) {
			return sess, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := c.Run(ctx, "do the thing", "build")
	if !errors.Is(err, ErrBypassNotAccepted) {
		t.Fatalf("err = %v, want ErrBypassNotAccepted", err)
	}
	if !IsBlockedOnPrompt(err) {
		t.Errorf("IsBlockedOnPrompt(%v) = false, want true", err)
	}
	if !res.IsError {
		t.Error("res.IsError = false, want true")
	}
}
