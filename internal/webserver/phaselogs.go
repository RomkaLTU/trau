package webserver

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// phaseLogBody carries a phase log's content on a write — it mirrors
// hubclient.phaseLogBody.
type phaseLogBody struct {
	Content string `json:"content"`
}

// phaseLogItem is one phase log in a list response — it mirrors hubclient.PhaseLog.
type phaseLogItem struct {
	Phase   string    `json:"phase"`
	Content string    `json:"content"`
	Updated time.Time `json:"updated"`
}

type phaseLogsResponse struct {
	Logs []phaseLogItem `json:"logs"`
}

// handleRunPhaseLog is the loop child's write seam for a single phase's log (ADR
// 0008): the child PUTs each phase's final output as the phase produces it, keyed
// by ticket and phase. Like an artifact write it comes from the live loop, so it
// is not refused while a loop is live.
func (s *Server) handleRunPhaseLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	phase := r.PathValue("phase")
	if phase == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing phase"})
		return
	}
	ticket := r.PathValue("ticket")
	s.importPhaseLogs(repo)
	var req phaseLogBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if err := s.stores.PhaseLogs().Upsert(repo.Root, ticket, phase, req.Content); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ticket": ticket, "phase": phase})
}

// handleRunPhaseLogs lists a ticket's phase logs for the inspector (GET,
// most-recently-written first) or drops them all (DELETE) — the reset/clear sweep.
func (s *Server) handleRunPhaseLogs(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	ticket := r.PathValue("ticket")
	s.importPhaseLogs(repo)
	logs := s.stores.PhaseLogs()

	switch r.Method {
	case http.MethodGet:
		rows, err := logs.List(repo.Root, ticket)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := phaseLogsResponse{Logs: make([]phaseLogItem, len(rows))}
		for i, row := range rows {
			out.Logs[i] = phaseLogItem{Phase: row.Phase, Content: row.Content, Updated: row.Updated}
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		if err := logs.Remove(repo.Root, ticket); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "ticket": ticket})
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// importPhaseLogs folds a repo's file-era phase logs into the authoritative table
// on first touch, best-effort. Like importArtifacts it skips a repo with a live
// loop — a legacy loop mid-migration may still be writing its logs — and leaves
// the files in place to retry on the next touch when an import fails.
func (s *Server) importPhaseLogs(repo registry.Repo) {
	if _, live := s.liveInstance(repo.Root); live {
		return
	}
	runsDir := repo.RunsDir
	if runsDir == "" {
		runsDir = repoRunsDir(repo.Root)
	}
	if err := s.stores.PhaseLogs().ImportLegacy(repo.Root, runsDir); err != nil {
		logger.Verbosef("import legacy phase logs %s: %v", repo.Name, err)
	}
}

// importAllPhaseLogs folds every known repo's file-era phase logs into the table,
// off any request path — the serve-startup counterpart to the per-repo lazy import.
func (s *Server) importAllPhaseLogs() {
	for _, repo := range s.knownRepos(registry.Live(s.home)) {
		s.importPhaseLogs(repo)
	}
}
