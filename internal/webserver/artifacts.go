package webserver

import (
	"encoding/json"
	"net/http"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// artifactBody carries a run artifact's content on the wire — it mirrors
// hubclient.artifactBody, both request and response.
type artifactBody struct {
	Content string `json:"content"`
}

// handleRunArtifact is the loop child's read/write seam for a single ticket's
// phase artifact — the handoff brief, verify rubric, verify verdict, or build
// notes (ADR 0008). The child never opens the database; it drives the
// authoritative artifacts table entirely through this endpoint. Writes come from
// the live loop itself, so unlike a checkpoint mutation it is not refused while a
// loop is live. On first touch of a repo any file-era artifacts are folded in.
func (s *Server) handleRunArtifact(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	kind := r.PathValue("kind")
	if !hubstore.ValidArtifactKind(kind) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown artifact kind"})
		return
	}
	ticket := r.PathValue("ticket")
	s.importArtifacts(repo)
	arts := s.stores.Artifacts()

	switch r.Method {
	case http.MethodGet:
		content, found, err := arts.One(repo.Root, ticket, kind)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown artifact"})
			return
		}
		writeJSON(w, http.StatusOK, artifactBody{Content: content})
	case http.MethodPut:
		var req artifactBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if err := arts.Upsert(repo.Root, ticket, kind, req.Content); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ticket": ticket, "kind": kind})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleRunArtifacts drops every artifact the hub holds for a ticket — the child's
// reset/clear/fresh-build sweep.
func (s *Server) handleRunArtifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	ticket := r.PathValue("ticket")
	s.importArtifacts(repo)
	if err := s.stores.Artifacts().Remove(repo.Root, ticket); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "ticket": ticket})
}

// importArtifacts folds a repo's file-era phase artifacts into the authoritative
// table on first touch, best-effort. Like importCheckpoints it skips a repo with a
// live loop — a legacy loop mid-migration may still be writing its files, so the
// hub never touches a live run's state — and leaves the files in place to retry on
// the next touch when an import fails.
func (s *Server) importArtifacts(repo registry.Repo) {
	if _, live := s.liveInstance(repo.Root); live {
		return
	}
	runsDir := repo.RunsDir
	if runsDir == "" {
		runsDir = repoRunsDir(repo.Root)
	}
	if err := s.stores.Artifacts().ImportLegacy(repo.Root, runsDir); err != nil {
		logger.Verbosef("import legacy artifacts %s: %v", repo.Name, err)
	}
}

// importAllArtifacts folds every known repo's file-era artifacts into the table,
// off any request path — the serve-startup counterpart to the per-repo lazy import.
func (s *Server) importAllArtifacts() {
	for _, repo := range s.knownRepos(s.liveInstances()) {
		s.importArtifacts(repo)
	}
}
