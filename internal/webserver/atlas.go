package webserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/RomkaLTU/trau/internal/hubatlas"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// atlasCatalogResponse is the Atlas catalog: each View with its latest document's
// metadata and staleness (GET /repos/{repo}/atlas).
type atlasCatalogResponse struct {
	Views []atlasCatalogView `json:"views"`
}

type atlasCatalogView struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Flavor      string   `json:"flavor"`
	HasDocument bool     `json:"has_document"`
	Version     int      `json:"version"`
	Commit      string   `json:"commit"`
	GeneratedAt string   `json:"generated_at"`
	CostUSD     *float64 `json:"cost_usd"`
	Error       string   `json:"error"`
	Stale       int      `json:"stale"`
}

// atlasDocumentResponse carries one View document's content
// (GET /repos/{repo}/atlas/{view}).
type atlasDocumentResponse struct {
	View        string          `json:"view"`
	Version     int             `json:"version"`
	Commit      string          `json:"commit"`
	GeneratedAt string          `json:"generated_at"`
	CostUSD     float64         `json:"cost_usd"`
	Document    json.RawMessage `json:"document"`
}

// handleAtlas serves the View catalog with each View's latest document metadata
// and staleness, for any registered repo.
func (s *Server) handleAtlas(w http.ResponseWriter, r *http.Request) {
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
	runs, err := s.stores.Checkpoints().All(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	views := make([]atlasCatalogView, 0, len(hubatlas.Catalog()))
	for _, v := range hubatlas.Catalog() {
		meta, err := s.stores.Atlas().Meta(repo.Root, v.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		cv := atlasCatalogView{
			ID:          v.ID,
			Title:       v.Title,
			Flavor:      string(v.Flavor),
			HasDocument: meta.HasDocument,
			Version:     meta.Version,
			Commit:      meta.Commit,
			GeneratedAt: meta.GeneratedAt,
			Error:       meta.Error,
		}
		if meta.HasDocument {
			cost := meta.CostUSD
			cv.CostUSD = &cost
			cv.Stale = atlasStaleness(runs, meta.GeneratedAt)
		}
		views = append(views, cv)
	}
	writeJSON(w, http.StatusOK, atlasCatalogResponse{Views: views})
}

// handleAtlasView serves a View's latest good document, or a specific version
// with ?version=.
func (s *Server) handleAtlasView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, viewID, ok := s.resolveAtlasView(w, r)
	if !ok {
		return
	}

	var (
		doc   hubstore.AtlasDocument
		found bool
		err   error
	)
	if raw := r.URL.Query().Get("version"); raw != "" {
		version, convErr := strconv.Atoi(raw)
		if convErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid version"})
			return
		}
		doc, found, err = s.stores.Atlas().Version(repo.Root, viewID, version)
	} else {
		doc, found, err = s.stores.Atlas().Latest(repo.Root, viewID)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found || doc.Document == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no document"})
		return
	}
	writeJSON(w, http.StatusOK, atlasDocumentResponse{
		View:        viewID,
		Version:     doc.Version,
		Commit:      doc.Commit,
		GeneratedAt: doc.CreatedAt,
		CostUSD:     doc.CostUSD,
		Document:    json.RawMessage(doc.Document),
	})
}

// handleAtlasGenerate spawns a one-shot headless generation for a View, returning
// 202 while it runs in the background, or 409 when a generation for the same (repo,
// view) is already in flight (ADR 0013). Generation is explicit-start only.
func (s *Server) handleAtlasGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, viewID, ok := s.resolveAtlasView(w, r)
	if !ok {
		return
	}
	view, _ := hubatlas.ViewByID(viewID)
	if !s.atlas.start(repo, view) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a generation for this view is already in progress"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "generating", "view": viewID})
}

// resolveAtlasView resolves the {repo} and {view} path segments, writing the 404
// and returning ok=false when either is unknown.
func (s *Server) resolveAtlasView(w http.ResponseWriter, r *http.Request) (repo registry.Repo, viewID string, ok bool) {
	repo, found := s.findRepo(r.PathValue("repo"))
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return repo, "", false
	}
	viewID = r.PathValue("view")
	if _, known := hubatlas.ViewByID(viewID); !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown view"})
		return repo, "", false
	}
	return repo, viewID, true
}

// atlasStaleness counts the merged runs recorded after generatedAt — the Atlas's
// staleness signal, derived from the checkpoints the hub already holds rather than
// git polling (ADR 0013). An unparseable or empty generatedAt yields 0.
func atlasStaleness(runs []hubstore.TicketCheckpoint, generatedAt string) int {
	since, ok := parseRunTime(generatedAt)
	if !ok {
		return 0
	}
	stale := 0
	for _, run := range runs {
		if run.Phase != state.Merged {
			continue
		}
		if ts, ok := parseRunTime(run.UpdatedAt); ok && ts.After(since) {
			stale++
		}
	}
	return stale
}

// parseRunTime parses a run-data timestamp, accepting the checkpoint store's
// zoneless layout and RFC3339. ok is false when neither parses.
func parseRunTime(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
