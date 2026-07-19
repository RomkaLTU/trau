package webserver

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
)

// takeoverStopTimeout bounds how long a takeover waits for the signalled loop to
// park before giving up without launching anything; takeoverPollInterval is the
// registry re-read cadence during that wait. Vars so tests can compress the wait.
var (
	takeoverStopTimeout  = 60 * time.Second
	takeoverPollInterval = 500 * time.Millisecond
)

// TakeoverResult is the outcome of a takeover request: whether a live loop was
// stopped on the way and whether the terminal launch was issued.
type TakeoverResult struct {
	Stopped bool `json:"stopped"`
	Opened  bool `json:"opened"`
}

// terminalLauncher is the hub's GUI seam, the launch counterpart to Supervisor:
// it isolates opening an OS terminal window so the takeover orchestration is
// testable without a GUI.
type terminalLauncher interface {
	Launch(ctx context.Context, app, command string) error
}

// osascriptLauncher is the production terminalLauncher: it drives the terminal
// app over osascript, so the wrapper starts in the window's own fresh login
// shell rather than inheriting the hub's environment.
type osascriptLauncher struct{}

func (osascriptLauncher) Launch(ctx context.Context, app, command string) error {
	out, err := exec.CommandContext(ctx, "osascript", osascriptArgs(app, command)...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("osascript: %w: %s", err, msg)
		}
		return fmt.Errorf("osascript: %w", err)
	}
	return nil
}

// osascriptArgs is the osascript argument vector that opens app running command
// in a new window and brings the app frontmost.
func osascriptArgs(app, command string) []string {
	quoted := appleScriptString(command)
	lines := []string{
		`tell application "Terminal" to do script ` + quoted,
		`tell application "Terminal" to activate`,
	}
	if app == "iTerm" {
		lines = []string{
			`tell application "iTerm" to create window with default profile command ` + quoted,
			`tell application "iTerm" to activate`,
		}
	}
	args := make([]string, 0, 2*len(lines))
	for _, line := range lines {
		args = append(args, "-e", line)
	}
	return args
}

// appleScriptString renders s as an AppleScript string literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// shellCommand renders argv as one shell command line, single-quoting every
// argument so paths with spaces survive the terminal's shell.
func shellCommand(argv ...string) string {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}

// handleRunTakeover hands a run to a human terminal (ADR 0018): it stops the
// ticket's live loop gracefully if one is working it, waits until the handoff
// is safe, then opens the configured macOS terminal running `trau takeover`,
// which resumes the run's recorded claude session. Conflicts — another ticket
// running, an existing takeover, no resumable session — refuse before anything
// is touched, and a stop that never settles launches nothing.
func (s *Server) handleRunTakeover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	if s.goos != "darwin" {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "terminal takeover needs a macOS hub"})
		return
	}
	ticket := r.PathValue("ticket")

	var run registry.Entry
	var hasRun bool
	root := filepath.Clean(repo.Root)
	for _, e := range s.liveInstances() {
		if filepath.Clean(e.RepoRoot) != root {
			continue
		}
		if e.SessionState == registry.StateTakeover {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":  fmt.Sprintf("%s is already taken over — PID %d holds %s in a terminal session", repo.Name, e.PID, e.Ticket),
				"reason": "already_taken_over",
			})
			return
		}
		switch e.SessionState {
		case registry.StateWorking, registry.StateGrazing, registry.StateStopping:
			if e.Ticket != ticket {
				busy := e.Ticket
				if busy == "" {
					busy = e.SessionState
				}
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":  fmt.Sprintf("%s is busy with %s (PID %d) — stop it before taking over %s", repo.Name, busy, e.PID, ticket),
					"reason": "repo_busy",
				})
				return
			}
			run, hasRun = e, true
		}
	}

	stopped := false
	if hasRun {
		if run.SessionState != registry.StateStopping {
			if err := s.sup.Signal(run.PID, syscall.SIGTERM); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to signal loop: " + err.Error()})
				return
			}
		}
		if !s.awaitParked(r.Context(), run.PID) {
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{
				"error": fmt.Sprintf("the loop (PID %d) did not stop within %s — nothing launched", run.PID, takeoverStopTimeout),
			})
			return
		}
		stopped = true
	}

	s.importCheckpoints(repo)
	row, found, err := s.stores.Checkpoints().One(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return
	}
	sid := checkpointField(row.Data, "SESSION")
	if sid == "" || !s.sessionExists(sid) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  fmt.Sprintf("no resumable claude session for %s", ticket),
			"reason": "no_resumable_session",
		})
		return
	}

	exe, err := os.Executable()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve hub binary: " + err.Error()})
		return
	}
	command := shellCommand(exe, "takeover", "--repo", repo.Root, ticket)
	if err := s.term.Launch(r.Context(), s.terminalApp(repo), command); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "open terminal failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, TakeoverResult{Stopped: stopped, Opened: true})
}

// awaitParked polls the registry until pid is gone or reports parked or idle —
// the states in which the ticket's checkpoint is safe to hand over — bounded by
// takeoverStopTimeout. False means the bound (or the request) expired first.
func (s *Server) awaitParked(ctx context.Context, pid int) bool {
	deadline := time.Now().Add(takeoverStopTimeout)
	for {
		settled := true
		for _, e := range s.liveInstances() {
			if e.PID == pid && e.SessionState != registry.StateParked && e.SessionState != registry.StateIdle {
				settled = false
			}
		}
		if settled {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(takeoverPollInterval):
		}
	}
}

// terminalApp is the repo's configured TERMINAL_APP, falling back to Terminal
// when the config cannot be read or leaves it unset.
func (s *Server) terminalApp(repo registry.Repo) string {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil || cfg.TerminalApp == "" {
		return "Terminal"
	}
	return cfg.TerminalApp
}
