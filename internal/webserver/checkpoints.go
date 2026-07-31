package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
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
	for _, repo := range s.knownRepos(s.liveInstances()) {
		s.importCheckpoints(repo)
	}
}

// mutableCheckpoint resolves the {repo} and {ticket} path segments for a
// checkpoint mutation and enforces the invariants every mutation shares: it 404s
// an unknown repo, refuses with a conflict while a loop is live in the repo — so
// the browser can never corrupt a running session's state — and 404s a ticket
// the checkpoints table has no row for. The live check must run before the
// existence lookup: importCheckpoints no-ops while a loop is live, so the table
// can still be missing a not-yet-imported checkpoint and would otherwise 404
// instead of reporting the conflict.
func (s *Server) mutableCheckpoint(w http.ResponseWriter, r *http.Request) (registry.Repo, string, bool) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return registry.Repo{}, "", false
	}
	if s.refuseWhenLive(w, repo) {
		return registry.Repo{}, "", false
	}
	ticket := r.PathValue("ticket")
	s.importCheckpoints(repo)
	if _, found, _ := s.stores.Checkpoints().One(repo.Root, ticket); !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return registry.Repo{}, "", false
	}
	return repo, ticket, true
}

// refuseWhenLive is the safety guard: a checkpoint mutation is refused with a
// conflict while a loop is live in the repo, so the browser can never corrupt a
// running session's state out from under it. It names the holding pid so the
// operator knows what to stop first. A takeover terminal gets its own reason:
// no web button can hand the repo back, only closing the session (ADR 0018).
func (s *Server) refuseWhenLive(w http.ResponseWriter, repo registry.Repo) bool {
	if e, ok := s.takeoverHolder(repo.Root); ok {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  fmt.Sprintf("%s is taken over — PID %d holds %s in a terminal session; close it first", repo.Name, e.PID, e.Ticket),
			"reason": "taken_over",
			"live":   true,
		})
		return true
	}
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
	for _, e := range s.liveInstances() {
		if filepath.Clean(e.RepoRoot) == cleaned {
			return e, true
		}
	}
	return registry.Entry{}, false
}
