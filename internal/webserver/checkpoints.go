package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// checkpointView is a ticket's checkpoint as the HTTP API returns it: the
// projected fields plus the full key set as a JSON object. It mirrors
// hubclient.Checkpoint on the wire.
type checkpointView struct {
	Ticket        string          `json:"ticket"`
	Phase         string          `json:"phase"`
	Title         string          `json:"title"`
	Branch        string          `json:"branch"`
	PR            string          `json:"pr"`
	PRURL         string          `json:"pr_url"`
	FailureReason string          `json:"failure_reason"`
	UpdatedAt     string          `json:"updated_at"`
	Data          json.RawMessage `json:"data"`
}

func newCheckpointView(ticket string, row hubstore.CheckpointRow) checkpointView {
	data := row.Data
	if data == "" {
		data = "{}"
	}
	return checkpointView{
		Ticket:        ticket,
		Phase:         row.Phase,
		Title:         row.Title,
		Branch:        row.Branch,
		PR:            row.PR,
		PRURL:         row.PRURL,
		FailureReason: row.FailureReason,
		UpdatedAt:     row.UpdatedAt,
		Data:          json.RawMessage(data),
	}
}

// checkpointPut is the body of a checkpoint write: the full key set the loop
// holds. The hub derives the projected columns from it.
type checkpointPut struct {
	Data map[string]string `json:"data"`
}

// handleRunCheckpoint is the loop child's read/write/delete seam for a single
// ticket's checkpoint (ADR 0008). The child never opens the database — it drives
// the authoritative checkpoints table entirely through this endpoint. On first
// touch of a repo any file-era state files are folded in and removed.
func (s *Server) handleRunCheckpoint(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	ticket := r.PathValue("ticket")
	s.importCheckpoints(repo)
	cps := s.stores.Checkpoints()

	switch r.Method {
	case http.MethodGet:
		row, found, err := cps.One(repo.Root, ticket)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown checkpoint"})
			return
		}
		writeJSON(w, http.StatusOK, newCheckpointView(ticket, row))
	case http.MethodPut:
		var req checkpointPut
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if err := cps.Upsert(repo.Root, ticket, req.Data); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ticket": ticket})
	case http.MethodDelete:
		if err := cps.Remove(repo.Root, ticket); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "ticket": ticket})
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleRepoCheckpoints lists every checkpoint the hub holds for a repo — the
// loop's whole-repo resume scan.
func (s *Server) handleRepoCheckpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	s.importCheckpoints(repo)
	rows, err := s.stores.Checkpoints().All(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	views := make([]checkpointView, 0, len(rows))
	for _, tc := range rows {
		views = append(views, newCheckpointView(tc.Ticket, tc.CheckpointRow))
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoints": views})
}

// importCheckpoints folds a repo's file-era checkpoints into the authoritative
// table on first touch, best-effort. It skips a repo with a live loop: the loop
// writes through the hub now, and a legacy loop mid-migration may still be
// writing its files, so the hub never touches a live run's state (the same
// invariant refuseWhenLive protects). A failed import leaves the files in place
// to retry on the next touch rather than failing the request.
func (s *Server) importCheckpoints(repo registry.Repo) {
	if _, live := s.liveInstance(repo.Root); live {
		return
	}
	runsDir := repo.RunsDir
	if runsDir == "" {
		runsDir = repoRunsDir(repo.Root)
	}
	if err := s.stores.Checkpoints().ImportLegacy(repo.Root, runsDir); err != nil {
		logger.Verbosef("import legacy checkpoints %s: %v", repo.Name, err)
	}
}

// importAllCheckpoints folds every known repo's file-era checkpoints into the
// table, off any request path — the serve-startup counterpart to the per-repo
// lazy import.
func (s *Server) importAllCheckpoints() {
	for _, repo := range s.knownRepos(registry.Live(s.home)) {
		s.importCheckpoints(repo)
	}
}

// resetTimeout bounds a reset: it drops the branch and re-queues the ticket on
// the tracker, so it must outlast a tracker write but never hang the request.
const resetTimeout = 2 * time.Minute

// reconcileTimeout bounds a reconcile: it cross-checks every in-flight
// checkpoint against the tracker, each query time-bounded inside the child, so
// the ceiling covers a repo with several stale rows.
const reconcileTimeout = 3 * time.Minute

// ResetRequest is the body of the reset endpoint. Force resets a ticket whose
// code is already merged — the same explicit override the CLI's --force is.
type ResetRequest struct {
	Force bool `json:"force"`
}

// ReconcileResult is the outcome of a reconcile: the tickets whose stale local
// checkpoint was dropped because the tracker now considers them finished.
type ReconcileResult struct {
	Repo       string   `json:"repo"`
	Reconciled []string `json:"reconciled"`
}

// handleResetRun resets a ticket the same way `trau --reset` does: it drops the
// branch and checkpoint and re-queues the ticket on the tracker. The mutation is
// refused while a live loop holds the repo, and resetting an already-merged
// ticket requires an explicit force — CLI parity on both counts.
func (s *Server) handleResetRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ticket, ok := s.checkpointTarget(w, r)
	if !ok {
		return
	}
	if s.refuseWhenLive(w, repo) {
		return
	}
	var req ResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if phase := s.stores.Checkpoints().Phase(repo.Root, ticket); phase == state.Merged && !req.Force {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":          fmt.Sprintf("%s is already merged — resetting it drops the shipped branch; confirm with force", ticket),
			"requires_force": true,
		})
		return
	}

	args := []string{"--repo", repo.Root, "--reset", ticket, "--no-tui"}
	if req.Force {
		args = append(args, "--force")
	}
	ctx, cancel := context.WithTimeout(r.Context(), resetTimeout)
	defer cancel()
	if _, err := s.sup.Capture(ctx, SpawnSpec{Dir: repo.Root, Args: args, Env: childEnv(s.home)}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "reset failed: " + err.Error()})
		return
	}
	if err := s.stores.Checkpoints().Remove(repo.Root, ticket); err != nil {
		logger.Verbosef("reset %s/%s: drop checkpoint: %v", repo.Name, ticket, err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset", "ticket": ticket})
}

// handleClearRun forgets a ticket's local checkpoint the same way `trau --clear`
// does: it drops only the durable state, never touching git or the tracker, for
// a ticket finished out-of-band. It is refused while a live loop holds the repo.
func (s *Server) handleClearRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ticket, ok := s.checkpointTarget(w, r)
	if !ok {
		return
	}
	if s.refuseWhenLive(w, repo) {
		return
	}
	was := s.stores.Checkpoints().Phase(repo.Root, ticket)
	if err := s.stores.Checkpoints().Remove(repo.Root, ticket); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "clear failed: " + err.Error()})
		return
	}
	if err := state.NewStore(repo.RunsDir).RemoveState(ticket); err != nil {
		logger.Verbosef("clear %s/%s: drop legacy state file: %v", repo.Name, ticket, err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared", "ticket": ticket, "was": was})
}

// handleReconcileRepo reconciles a repo's checkpoints against the tracker on
// demand, driving `trau --status --json`, which drops any in-flight or
// quarantined checkpoint whose issue the tracker now reports as terminal. Like
// every checkpoint mutation it is refused while a live loop holds the repo.
func (s *Server) handleReconcileRepo(w http.ResponseWriter, r *http.Request) {
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
	if s.refuseWhenLive(w, repo) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reconcileTimeout)
	defer cancel()
	out, err := s.sup.Capture(ctx, SpawnSpec{
		Dir:  repo.Root,
		Args: []string{"--repo", repo.Root, "--status", "--json", "--no-tui"},
		Env:  childEnv(s.home),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "reconcile failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ReconcileResult{Repo: repo.Name, Reconciled: parseReconciled(out)})
}

// checkpointTarget resolves the {repo} and {ticket} path segments to a known repo
// and an existing run, writing the 404 and reporting false when either misses so
// a mutation never runs against a repo the hub does not own or a ticket with no
// checkpoint.
func (s *Server) checkpointTarget(w http.ResponseWriter, r *http.Request) (registry.Repo, string, bool) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return registry.Repo{}, "", false
	}
	ticket := r.PathValue("ticket")
	s.importCheckpoints(repo)
	if _, found, _ := s.stores.Checkpoints().One(repo.Root, ticket); !found && !runExists(repo.RunsDir, ticket) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return registry.Repo{}, "", false
	}
	return repo, ticket, true
}

// refuseWhenLive is the safety guard: a checkpoint mutation is refused with a
// conflict while a loop is live in the repo, so the browser can never corrupt a
// running session's state out from under it. It names the holding pid so the
// operator knows what to stop first.
func (s *Server) refuseWhenLive(w http.ResponseWriter, repo registry.Repo) bool {
	e, ok := s.liveInstance(repo.Root)
	if !ok {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": fmt.Sprintf("%s has a live loop (pid %d) — stop it before changing its checkpoints", repo.Name, e.PID),
		"live":  true,
	})
	return true
}

// liveInstance reports the live registry entry whose loop is running in root, if
// any. Roots are cleaned before comparison so a trailing slash never hides a
// running loop.
func (s *Server) liveInstance(root string) (registry.Entry, bool) {
	cleaned := filepath.Clean(root)
	for _, e := range registry.Live(s.home) {
		if filepath.Clean(e.RepoRoot) == cleaned {
			return e, true
		}
	}
	return registry.Entry{}, false
}

// parseReconciled pulls the reconciled ticket list out of a `--status --json`
// document. A shape it cannot parse yields no reconciled tickets rather than an
// error — the reconcile still ran in the child; this only reports what it dropped.
func parseReconciled(stdout []byte) []string {
	var report struct {
		Reconciled []string `json:"reconciled"`
	}
	if err := json.Unmarshal(stdout, &report); err != nil {
		return []string{}
	}
	if report.Reconciled == nil {
		return []string{}
	}
	return report.Reconciled
}
