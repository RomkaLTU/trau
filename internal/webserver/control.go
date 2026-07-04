package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
)

// dryRunTimeout bounds a preview: it drives a fresh trau to ask the tracker for
// the next eligible ticket, so it must outlast an MCP pick but never hang the
// request forever.
const dryRunTimeout = 2 * time.Minute

// reTicketID matches a bare tracker identifier of any prefix (ACME-42, TMS-456).
// The exact prefix is validated against the target repo's config by the spawned
// loop; the hub only rejects shapes that are clearly not a ticket before it
// bothers launching a run for them.
var reTicketID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// StartRequest is the body of POST /api/v1/instances. Repo names the loop's
// target, either by its allowlisted root or its base name. The optional targets
// mirror the CLI: Ticket runs one specific ticket (the --once equivalent), Epic
// drives an epic's sub-issues (the --parent equivalent); they are mutually
// exclusive. Provider is an ephemeral per-run override of the configured routing
// — it applies only to this spawn and never persists to config.
type StartRequest struct {
	Repo     string `json:"repo"`
	Ticket   string `json:"ticket,omitempty"`
	Epic     string `json:"epic,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// StartResult is returned when the hub spawns a loop, carrying the child's PID so
// the caller can correlate it with the instance that self-registers moments later.
type StartResult struct {
	PID      int    `json:"pid"`
	Repo     string `json:"repo"`
	RepoRoot string `json:"repo_root"`
}

// startInstance spawns a headless loop in an allowlisted repo. Repos outside the
// workspace allowlist are observe-only and refused with a clear error, so the
// write path can never launch a loop somewhere the operator hasn't sanctioned.
func (s *Server) startInstance(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Repo) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}
	ticket := strings.TrimSpace(req.Ticket)
	epic := strings.TrimSpace(req.Epic)
	if req.Ticket != "" && ticket == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket must not be blank"})
		return
	}
	if req.Epic != "" && epic == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "epic must not be blank"})
		return
	}
	if ticket != "" && epic != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket and epic are mutually exclusive"})
		return
	}
	if ticket != "" && !reTicketID.MatchString(ticket) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("ticket %q is not a valid ticket identifier", req.Ticket)})
		return
	}
	if epic != "" && !reTicketID.MatchString(epic) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("epic %q is not a valid ticket identifier", req.Epic)})
		return
	}
	root, ok := s.allowedRoot(req.Repo)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is not on the serve workspace allowlist and is observe-only; add its root to SERVE_WORKSPACE to start loops there", req.Repo),
		})
		return
	}

	args := []string{"--repo", root, "--no-tui"}
	switch {
	case ticket != "":
		args = append(args, "--parent", ticket, "--once")
	case epic != "":
		args = append(args, "--parent", epic)
	}
	if provider := strings.TrimSpace(req.Provider); provider != "" {
		args = append(args, "--provider", provider)
	}

	pid, err := s.sup.Spawn(SpawnSpec{
		Dir:  root,
		Args: args,
		Env:  childEnv(s.home),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start loop: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, StartResult{PID: pid, Repo: filepath.Base(root), RepoRoot: root})
}

// handleStopInstance sends SIGTERM to a registered loop, hub-started or not, so a
// web stop flows through the same graceful shutdown as Ctrl-C and in-flight work
// checkpoints identically. Only a currently-registered PID can be stopped, which
// keeps the endpoint from being a general process killer.
func (s *Server) handleStopInstance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pid"})
		return
	}
	if !s.registered(pid) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no live instance with that pid"})
		return
	}
	if err := s.sup.Signal(pid, syscall.SIGTERM); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to signal loop: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping", "signal": "SIGTERM"})
}

// DryRunResult is the outcome of a preview: the next eligible ticket for a repo,
// or an empty Ticket when nothing is eligible. It is produced with zero side
// effects — no branch, no checkpoint, no tracker change.
type DryRunResult struct {
	Repo     string `json:"repo"`
	RepoRoot string `json:"repo_root"`
	Ticket   string `json:"ticket"`
}

// handleDryRun previews the next eligible ticket for an allowlisted repo by
// driving a fresh trau with --dry-run, which picks without touching anything, and
// returning what it would have run next. It is gated on the workspace allowlist
// like a start: previewing still runs the binary in the repo.
func (s *Server) handleDryRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	root, ok := s.allowedRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is not on the serve workspace allowlist and is observe-only; add its root to SERVE_WORKSPACE to preview runs there", r.PathValue("repo")),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), dryRunTimeout)
	defer cancel()
	out, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  root,
		Args: []string{"--repo", root, "--dry-run", "--no-tui"},
		Env:  childEnv(s.home),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "dry-run failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, DryRunResult{Repo: filepath.Base(root), RepoRoot: root, Ticket: parseNextTicket(out)})
}

// parseNextTicket extracts the ticket a --dry-run reported from its stdout. The
// plain-mode console line is a stable "HH:MM:SS Next up: <ID>"; anything else
// (including "Nothing eligible") yields an empty string.
func parseNextTicket(stdout []byte) string {
	const marker = "Next up:"
	for _, line := range strings.Split(string(stdout), "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			return strings.TrimSpace(line[i+len(marker):])
		}
	}
	return ""
}

func (s *Server) registered(pid int) bool {
	for _, e := range registry.Live(s.home) {
		if e.PID == pid {
			return true
		}
	}
	return false
}

// allows reports whether the hub may start a loop in root.
func (s *Server) allows(root string) bool {
	target := filepath.Clean(root)
	for _, r := range s.workspace {
		if r == target {
			return true
		}
	}
	return false
}

// allowedRoot resolves a start request's repo identifier to an allowlisted root.
// It matches an allowlisted root path exactly, or an unambiguous base name, so
// the UI can start a loop by either the path it shows or the short repo name.
func (s *Server) allowedRoot(ident string) (string, bool) {
	ident = strings.TrimSpace(ident)
	cleaned := filepath.Clean(ident)
	for _, r := range s.workspace {
		if r == cleaned {
			return r, true
		}
	}
	var match string
	for _, r := range s.workspace {
		if filepath.Base(r) == ident {
			if match != "" {
				return "", false
			}
			match = r
		}
	}
	return match, match != ""
}

// normalizeRoots cleans and de-duplicates the configured workspace roots while
// preserving order, so allowlist comparisons are path-stable.
func normalizeRoots(roots []string) []string {
	if len(roots) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(roots))
	out := make([]string, 0, len(roots))
	for _, raw := range roots {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

// workspaceRepo synthesizes a RepoView entry for an allowlisted repo the hub has
// never seen run, so it is startable before its first loop registers.
func workspaceRepo(root string) registry.Repo {
	return registry.Repo{
		Name:    filepath.Base(root),
		Root:    root,
		RunsDir: filepath.Join(root, ".trau", "runs"),
	}
}

// childEnv is the environment a spawned loop inherits, pinned to the hub's trau
// home so the child registers into the same registry the hub reads.
func childEnv(home string) []string {
	env := os.Environ()
	if home == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRAU_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TRAU_HOME="+home)
}
