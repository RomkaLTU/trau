package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// AppURLRequest is the body of an app URL create or update: the URL browser
// verify drives, a human label, and the workspace whose app it serves. A blank
// workspace files the repo's default target, of which there is at most one.
type AppURLRequest struct {
	Label     string `json:"label"`
	URL       string `json:"url"`
	Workspace string `json:"workspace"`
}

// AppURLView is an app URL entry as every reader sees it — the settings surface
// and the loop's run-start fetch alike. Nothing here is secret, so there is one
// shape rather than the masked/unmasked pair QA accounts need.
type AppURLView struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	URL       string `json:"url"`
	Workspace string `json:"workspace"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// handleAppURLs lists (GET) or creates (POST) the repo's app URL entries.
func (s *Server) handleAppURLs(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listAppURLs(w, repo)
	case http.MethodPost:
		s.createAppURL(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listAppURLs(w http.ResponseWriter, repo registry.Repo) {
	entries, err := s.stores.AppURLs().List(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list app urls: " + err.Error()})
		return
	}
	views := make([]AppURLView, 0, len(entries))
	for _, e := range entries {
		views = append(views, appURLView(e))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) createAppURL(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	req, ok := decodeAppURL(w, r)
	if !ok {
		return
	}
	if !s.appURLWorkspaceFree(w, repo, req.Workspace) {
		return
	}
	e, err := s.stores.AppURLs().Create(repo.Root, appURLInput(req))
	if errors.Is(err, hubstore.ErrAppURLWorkspaceTaken) {
		writeAppURLTaken(w, req.Workspace)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create app url: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, appURLView(e))
}

// handleAppURL reads (GET), replaces (PUT), or removes (DELETE) a single app URL
// entry.
func (s *Server) handleAppURL(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id, ok := appURLID(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getAppURL(w, repo, id)
	case http.MethodPut:
		s.updateAppURL(w, r, repo, id)
	case http.MethodDelete:
		s.deleteAppURL(w, repo, id)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) getAppURL(w http.ResponseWriter, repo registry.Repo, id int64) {
	e, found, err := s.stores.AppURLs().Get(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read app url: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown app url"})
		return
	}
	writeJSON(w, http.StatusOK, appURLView(e))
}

func (s *Server) updateAppURL(w http.ResponseWriter, r *http.Request, repo registry.Repo, id int64) {
	existing, found, err := s.stores.AppURLs().Get(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read app url: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown app url"})
		return
	}
	req, ok := decodeAppURL(w, r)
	if !ok {
		return
	}
	if req.Workspace != existing.Workspace && !s.appURLWorkspaceFree(w, repo, req.Workspace) {
		return
	}
	e, err := s.stores.AppURLs().Update(repo.Root, id, appURLInput(req))
	if errors.Is(err, hubstore.ErrAppURLNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown app url"})
		return
	}
	if errors.Is(err, hubstore.ErrAppURLWorkspaceTaken) {
		writeAppURLTaken(w, req.Workspace)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update app url: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, appURLView(e))
}

func (s *Server) deleteAppURL(w http.ResponseWriter, repo registry.Repo, id int64) {
	deleted, err := s.stores.AppURLs().Delete(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete app url: " + err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown app url"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// appURLWorkspaceFree reports whether workspace is still unclaimed in the repo,
// answering 409 when it is not — a second entry for the same workspace, or a
// second default when workspace is blank.
func (s *Server) appURLWorkspaceFree(w http.ResponseWriter, repo registry.Repo, workspace string) bool {
	_, exists, err := s.stores.AppURLs().ByWorkspace(repo.Root, workspace)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "check app url: " + err.Error()})
		return false
	}
	if !exists {
		return true
	}
	writeAppURLTaken(w, workspace)
	return false
}

// writeAppURLTaken answers the 409 for a workspace that already holds an entry,
// whether the pre-check saw it or the store's constraint caught the write.
func writeAppURLTaken(w http.ResponseWriter, workspace string) {
	msg := "a default app URL already exists for this repo — give this entry a workspace or edit the existing one"
	if workspace != "" {
		msg = "an app URL for workspace " + strconv.Quote(workspace) + " already exists"
	}
	writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
}

func decodeAppURL(w http.ResponseWriter, r *http.Request) (AppURLRequest, bool) {
	var req AppURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return AppURLRequest{}, false
	}
	req.Label = strings.TrimSpace(req.Label)
	req.URL = strings.TrimSpace(req.URL)
	req.Workspace = strings.TrimSpace(req.Workspace)
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": hubstore.ErrAppURLRequired.Error()})
		return AppURLRequest{}, false
	}
	return req, true
}

func appURLID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "app url id must be a number"})
		return 0, false
	}
	return id, true
}

func appURLInput(req AppURLRequest) hubstore.AppURLInput {
	return hubstore.AppURLInput{Label: req.Label, URL: req.URL, Workspace: req.Workspace}
}

func appURLView(e hubstore.AppURL) AppURLView {
	return AppURLView{
		ID:        e.ID,
		Label:     e.Label,
		URL:       e.URL,
		Workspace: e.Workspace,
		CreatedAt: e.CreatedAt,
		UpdatedAt: e.UpdatedAt,
	}
}
