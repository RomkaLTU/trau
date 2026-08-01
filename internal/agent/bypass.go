package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/RomkaLTU/trau/internal/sanitize"
)

// ErrBypassNotAccepted marks a claude child that raised the one-time
// --dangerously-skip-permissions acknowledgment dialog instead of starting
// work. trau never answers that dialog on the operator's behalf — unlike the
// directory-trust prompt it auto-confirms, this one is a deliberate
// person-accepts-the-risk gate (COD-1326) — so the run fails fast and names
// the one command that clears it for the whole machine.
var ErrBypassNotAccepted = errors.New("claude has not recorded this machine's one-time bypass-permissions acknowledgment — run `claude --dangerously-skip-permissions` once, accept, then resume")

// ErrInteractivePrompt marks a child that stopped on a blocking question no one
// in an unattended run can answer: a selection dialog was on screen and the
// output then went quiet (or the process exited on it). The transcript shows
// the exact question; clearing it means running the provider CLI once by hand.
var ErrInteractivePrompt = errors.New("agent is waiting on an interactive prompt an unattended run cannot answer — open the transcript (trau watch) for the question, clear it by running the provider CLI once by hand, then resume")

// IsBlockedOnPrompt reports whether err is (or wraps) either blocked-on-a-dialog
// sentinel. The pipeline folds these into the same blameless pause as an auth
// wall: retrying re-raises the same dialog, and the fix is a human at the
// console once.
func IsBlockedOnPrompt(err error) bool {
	return errors.Is(err, ErrBypassNotAccepted) || errors.Is(err, ErrInteractivePrompt)
}

// ClaudeBypassAccepted reports whether this machine has recorded Claude Code's
// one-time acceptance of --dangerously-skip-permissions
// (bypassPermissionsModeAccepted in its global config). A missing config file
// is a plain false — claude has simply never been accepted here; the error is
// reserved for an unreadable or unparsable file, which preflight callers
// report as "could not verify" rather than as a failed check.
func ClaudeBypassAccepted() (bool, error) {
	path, err := claudeGlobalConfigPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var state struct {
		Accepted bool `json:"bypassPermissionsModeAccepted"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	return state.Accepted, nil
}

// ClaudeFirstRun is what Claude Code's global config records about the
// interactive first run it demands on every new machine: the onboarding wizard
// (theme, fullscreen renderer) and a login. Both dialogs open on the pty trau
// drives, where no one can answer them.
type ClaudeFirstRun struct {
	OnboardingCompleted bool
	LoggedIn            bool
}

// ClaudeFirstRunState reads that state from the same global config
// ClaudeBypassAccepted reads. A missing config file is the never-run-here case
// and comes back zero-valued rather than as an error, which is reserved for an
// unreadable or unparsable file.
func ClaudeFirstRunState() (ClaudeFirstRun, error) {
	path, err := claudeGlobalConfigPath()
	if err != nil {
		return ClaudeFirstRun{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ClaudeFirstRun{}, nil
	}
	if err != nil {
		return ClaudeFirstRun{}, err
	}
	var state struct {
		OnboardingCompleted bool           `json:"hasCompletedOnboarding"`
		OAuthAccount        map[string]any `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ClaudeFirstRun{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return ClaudeFirstRun{
		OnboardingCompleted: state.OnboardingCompleted,
		LoggedIn:            len(state.OAuthAccount) > 0,
	}, nil
}

// claudeGlobalConfigPath is where Claude Code keeps its per-machine state
// (.claude.json). By default the file sits in the home directory as a sibling
// of the ~/.claude dir — not inside it — and moves into CLAUDE_CONFIG_DIR when
// that is set.
func claudeGlobalConfigPath() (string, error) {
	if strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) != "" {
		return filepath.Join(claudeConfigDir(), ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// hasClaudeBypassPrompt matches the bypass acknowledgment dialog ("WARNING:
// Claude Code running in Bypass Permissions mode … Yes, I accept"). Both the
// mode name and the accept verb are required, and the sighting is debounced
// like the auth wall: agent prose can quote these exact strings — a ticket
// about this very dialog does — but prose keeps scrolling while the real
// dialog sits on a quiet screen.
func hasClaudeBypassPrompt(s string) bool {
	flat := sanitize.StripANSI(s)
	return strings.Contains(flat, "Bypass Permissions") && strings.Contains(flat, "accept")
}

// menuPointerRe is a selection pointer on an enumerated option — the shape the
// claude-style TUIs draw every blocking question with ("❯ 1. No, exit"). A
// composer's bare pointer carries no numbered option, so it does not match.
var menuPointerRe = regexp.MustCompile(`[❯›]\s*\d+\.`)

// hasInteractiveMenu matches any blocking selection dialog, whatever question
// it asks — the catch-all beneath the specific matchers, debounced for the
// same reason they are. Codex is deliberately excluded: its TUI animates
// behind dialogs, so a quiet window never comes and its known screens have
// exact instant matchers instead (hasCodexTrustPrompt, hasCodexAuthWall).
func hasInteractiveMenu(s string) bool {
	return menuPointerRe.MatchString(sanitize.StripANSI(s))
}
