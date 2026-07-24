package webserver

import (
	"bytes"
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

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// dryRunTimeout bounds a preview: it drives a fresh trau to ask the tracker for
// the next eligible ticket, so it must outlast an MCP pick but never hang the
// request forever.
const dryRunTimeout = 2 * time.Minute

// eligibleTimeout bounds an eligible-ticket listing: it drives a fresh trau to
// enumerate the repo's ready queue through the tracker, so it must outlast a
// tracker query but never hang the request.
const eligibleTimeout = 2 * time.Minute

// epicPreviewTimeout bounds an epic sub-issue preview: it drives a fresh trau to
// list an epic's children through the tracker, so it must outlast a tracker query
// but never hang the request.
const epicPreviewTimeout = 2 * time.Minute

// reTicketID matches a bare tracker identifier of any prefix (ACME-42, TMS-456).
// The exact prefix is validated against the target repo's config by the spawned
// loop; the hub only rejects shapes that are clearly not a ticket before it
// bothers launching a run for them.
var reTicketID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

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

// stopWaitPoll is the process-liveness poll cadence stopAndWait uses while
// waiting out a grace period and while confirming a kill took. stopKillConfirm
// bounds that post-kill confirmation — SIGKILL cannot be caught, so this only
// covers the process's actual exit, never a resumed run. Both are vars so
// tests compress them instead of sleeping for real.
var (
	stopWaitPoll    = 200 * time.Millisecond
	stopKillConfirm = 5 * time.Second
)

// stopAndWait stops pid with escalation and is guaranteed to end it: SIGTERM,
// then wait out grace for the loop to exit on its own, then a group SIGKILL if
// it is still alive, confirming the process is gone before returning. A stale
// or already-dead pid succeeds immediately without signalling anything — the
// caller's goal (pid not running) is already met. Either way, once the process
// is confirmed gone it settles the hub's own records for a run that never got
// to report its own terminal state (settleStoppedRun) — always true after an
// escalation, and also true for a loop that died some other way without
// deregistering; a loop that exited and deregistered on its own is untouched.
func (s *Server) stopAndWait(pid int, grace time.Duration) error {
	if !registry.Alive(pid) {
		s.settleStoppedRun(pid)
		return nil
	}
	if err := s.sup.Signal(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	if !awaitDead(pid, grace) {
		if err := s.sup.Kill(pid); err != nil {
			return fmt.Errorf("kill pid %d: %w", pid, err)
		}
		if !awaitDead(pid, stopKillConfirm) {
			return fmt.Errorf("pid %d still alive after a group SIGKILL", pid)
		}
	}
	s.settleStoppedRun(pid)
	return nil
}

// awaitDead polls pid's process liveness until it is gone or within elapses.
func awaitDead(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for registry.Alive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(stopWaitPoll)
	}
	return true
}

// settleStoppedRun settles the hub's own records for pid once its process is
// confirmed gone. A loop that deregistered cleanly on its own (the ordinary
// SIGTERM path) has already removed its registry entry, so this is a no-op;
// one that never got the chance to — force-killed, or dead some other way
// without reporting — still has one, which this drops, and its checkpoint (if
// it was mid-ticket) gets stamped stopped/shutdown so nothing keeps showing it
// as running.
func (s *Server) settleStoppedRun(pid int) {
	entry, found, err := s.stores.Instances().Get(pid)
	if err != nil || !found {
		return
	}
	if err := s.stores.Instances().Remove(pid); err != nil {
		logger.Verbosef("settle stopped pid %d: remove registry entry: %v", pid, err)
	}
	if entry.Ticket == "" {
		return
	}
	s.stampCheckpointStopped(entry.RepoRoot, entry.Ticket)
}

// stampCheckpointStopped marks ticket's checkpoint stopped by the hub itself,
// preserving every other field, so a run that never got to report its own
// outcome doesn't read as a phantom in-flight build. A ticket with no
// checkpoint yet has nothing to stamp.
func (s *Server) stampCheckpointStopped(root, ticket string) {
	row, found, err := s.stores.Checkpoints().One(root, ticket)
	if err != nil || !found {
		return
	}
	data := map[string]string{}
	if row.Data != "" {
		_ = json.Unmarshal([]byte(row.Data), &data)
	}
	data["FAILURE_CLASS"] = state.FailStopped
	data["FAILURE_REASON"] = "shutdown"
	data["UPDATED"] = time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := s.stores.Checkpoints().Upsert(root, ticket, data); err != nil {
		logger.Verbosef("stamp stopped checkpoint %s/%s: %v", root, ticket, err)
	}
}

// RestartAck is the answer to an accepted restart: the version that is on its
// way out, so a client can tell it apart from the one that comes back.
type RestartAck struct {
	Restarting bool   `json:"restarting"`
	Version    string `json:"version"`
}

// EnableRestart wires the restart endpoint to fn, which the serve command
// implements as shutdown-then-respawn. fn must return promptly — it is called
// from the request goroutine, and a graceful shutdown waits on that request —
// so it signals the restart rather than performing it. Without it the endpoint
// answers 503: a hub embedded in something other than `trau serve` has no
// successor to spawn.
func (s *Server) EnableRestart(fn func()) {
	s.restart = fn
}

// handleHubRestart acknowledges before restarting, so the caller learns the
// outgoing version over a connection that is about to close. It restarts
// unconditionally — the warn-and-confirm lives in the clients — but only once:
// a second POST arriving during the drain is acknowledged without spawning a
// second successor.
func (s *Server) handleHubRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.restart == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this hub cannot restart itself"})
		return
	}
	writeJSON(w, http.StatusAccepted, RestartAck{Restarting: true, Version: s.version})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.triggerRestart()
}

// triggerRestart runs the restart hook at most once and reports whether this hub
// can restart itself at all. A hub without one — one embedded in something other
// than `trau serve` — keeps serving; a one-click update that lands there still
// upgrades the binary and leaves a restart pending.
func (s *Server) triggerRestart() bool {
	if s.restart == nil {
		return false
	}
	s.restartOnce.Do(s.restart)
	return true
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

// EligibleTicket is one ready ticket a repo could pick next: its identifier,
// title, label names, immediate epic parent (empty for a top-level ticket), and
// whether it is itself a parent/epic. It powers the Loop card's ready-queue
// preview and Add all eligible, and can group sub-issues under their epic.
type EligibleTicket struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent"`
	HasChildren bool     `json:"has_children"`
}

// EligibleResult is the outcome of an eligible-ticket listing: the repo and its
// ready queue, empty when nothing is eligible.
type EligibleResult struct {
	Repo     string           `json:"repo"`
	RepoRoot string           `json:"repo_root"`
	Tickets  []EligibleTicket `json:"tickets"`
}

// handleEligible lists an allowlisted repo's eligible ready tickets by driving a
// fresh trau with --list-eligible --json and returning what it enumerated. Like a
// dry-run it is gated on the workspace allowlist — listing still runs the binary
// in the repo — and reads only: it never spawns a loop or touches the tracker.
func (s *Server) handleEligible(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	root, ok := s.allowedRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is not on the serve workspace allowlist and is observe-only; add its root to SERVE_WORKSPACE to list its eligible tickets", r.PathValue("repo")),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), eligibleTimeout)
	defer cancel()
	out, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  root,
		Args: []string{"--repo", root, "--list-eligible", "--json", "--no-tui"},
		Env:  childEnv(s.home),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "listing eligible tickets failed: " + err.Error()})
		return
	}
	tickets, err := parseEligibleTickets(out)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "listing eligible tickets failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, EligibleResult{Repo: filepath.Base(root), RepoRoot: root, Tickets: tickets})
}

// parseEligibleTickets decodes the JSON array a --list-eligible --json emitted on
// stdout. Empty output means an empty queue; a body that is not the expected JSON
// array is an error so a broken capture surfaces cleanly instead of as no tickets.
func parseEligibleTickets(stdout []byte) ([]EligibleTicket, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return []EligibleTicket{}, nil
	}
	var tickets []EligibleTicket
	if err := json.Unmarshal(trimmed, &tickets); err != nil {
		return nil, fmt.Errorf("unexpected eligible-ticket output")
	}
	out := make([]EligibleTicket, 0, len(tickets))
	for _, t := range tickets {
		if t.Labels == nil {
			t.Labels = []string{}
		}
		out = append(out, t)
	}
	return out, nil
}

// EpicSubIssue is one child of an epic: its identifier, title, and preview state
// (done, epic for a nested parent, or todo for a buildable child).
type EpicSubIssue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// EpicPreviewResult is the outcome of an epic preview: the repo, the previewed
// epic, and its direct sub-issues, empty when the epic has no children.
type EpicPreviewResult struct {
	Repo      string         `json:"repo"`
	RepoRoot  string         `json:"repo_root"`
	Epic      string         `json:"epic"`
	SubIssues []EpicSubIssue `json:"sub_issues"`
}

// handleEpicPreview lists an allowlisted repo's epic sub-issues and their states
// by driving a fresh trau with --list-epic <id> --json. Like a dry-run it is
// gated on the workspace allowlist — previewing still runs the binary in the repo
// — and reads only: it never spawns a loop or touches the tracker. It powers the
// Loop screen's epic scoping, so the operator sees what an epic contains before
// launching a run against it.
func (s *Server) handleEpicPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	epic := strings.TrimSpace(r.PathValue("epic"))
	if !reTicketID.MatchString(epic) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("epic %q is not a valid ticket identifier", epic)})
		return
	}
	root, ok := s.allowedRoot(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("repo %q is not on the serve workspace allowlist and is observe-only; add its root to SERVE_WORKSPACE to preview its epics", r.PathValue("repo")),
		})
		return
	}

	subs, err := s.listEpicSubIssues(r.Context(), root, epic)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "epic preview failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, EpicPreviewResult{Repo: filepath.Base(root), RepoRoot: root, Epic: epic, SubIssues: subs})
}

// listEpicSubIssues drives a fresh trau with --list-epic <id> --json in root and
// decodes the sub-issues it prints. It is the read-only preview the epic preview
// endpoint and Queue registration share, so an epic reads the same wherever the
// hub surfaces it.
func (s *Server) listEpicSubIssues(ctx context.Context, root, epic string) ([]EpicSubIssue, error) {
	ctx, cancel := context.WithTimeout(ctx, epicPreviewTimeout)
	defer cancel()
	out, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  root,
		Args: []string{"--repo", root, "--list-epic", epic, "--json", "--no-tui"},
		Env:  childEnv(s.home),
	})
	if err != nil {
		return nil, err
	}
	return parseEpicSubIssues(out)
}

// parseEpicSubIssues decodes the JSON array a --list-epic --json emitted on
// stdout. Empty output means an epic with no children; a body that is not the
// expected JSON array is an error so a broken capture surfaces cleanly instead of
// as no sub-issues.
func parseEpicSubIssues(stdout []byte) ([]EpicSubIssue, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return []EpicSubIssue{}, nil
	}
	var subs []EpicSubIssue
	if err := json.Unmarshal(trimmed, &subs); err != nil {
		return nil, fmt.Errorf("unexpected epic sub-issue output")
	}
	if subs == nil {
		subs = []EpicSubIssue{}
	}
	return subs, nil
}

func (s *Server) registered(pid int) bool {
	for _, e := range s.liveInstances() {
		if e.PID == pid {
			return true
		}
	}
	return false
}

// allowedRoot resolves a start request's repo identifier to an allowlisted root.
// It matches an allowlisted root path exactly, or an unambiguous base name, so
// the UI can start a loop by either the path it shows or the short repo name. The
// allowlist is the effective merge of the SERVE_WORKSPACE seed and the registered
// set, read per request so a just-registered repo is startable without a restart.
func (s *Server) allowedRoot(ident string) (string, bool) {
	return matchRoot(s.effectiveRoots(), ident)
}

// matchRoot resolves a repo identifier against a set of roots: an exact cleaned
// path, or an unambiguous base name. An ambiguous base name matches nothing, so
// a caller never acts on the wrong repo when two roots share a directory name.
func matchRoot(roots []string, ident string) (string, bool) {
	ident = strings.TrimSpace(ident)
	if ident == "" {
		return "", false
	}
	cleaned := filepath.Clean(ident)
	for _, r := range roots {
		if r == cleaned {
			return r, true
		}
	}
	var match string
	for _, r := range roots {
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
		RunsDir: repoRunsDir(root),
	}
}

// childEnv is the environment a spawned loop inherits, pinned to the hub's trau
// home so the child registers into the same registry the hub reads. TRAU_ACTIVE
// is stripped: the hub may carry it from the loop that started it, but hub
// spawns are deliberate top-level runs, and inheriting the marker would trip the
// child's nested-loop guard. Claude Code session markers are stripped too: a
// hub started from inside a Claude Code session would otherwise hand every
// agent child CLAUDE_CODE_CHILD_SESSION, silently disabling transcript saving.
func childEnv(home string) []string {
	env := agent.ScrubClaudeSessionEnv(os.Environ())
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TRAU_ACTIVE=") {
			continue
		}
		if home != "" && strings.HasPrefix(kv, "TRAU_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	if home != "" {
		out = append(out, "TRAU_HOME="+home)
	}
	return out
}
