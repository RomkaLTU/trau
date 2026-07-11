package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// InternalIssueRequest is the body of POST /repos/{repo}/issues/internal and
// PATCH /repos/{repo}/issues/internal/{id}: the editable content of an internal
// issue — title, markdown description, workflow state (a status group like
// backlog|started|done), labels, and an optional parent identifier nesting it
// under an epic.
type InternalIssueRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent"`
}

// InternalIssueResponse is a stored internal issue as the create/edit forms and
// the board read it: its allocated identifier, content, normalized state and its
// display status, source, and whether it heads an epic.
type InternalIssueResponse struct {
	Repo        string   `json:"repo"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent,omitempty"`
	Source      string   `json:"source"`
	HasChildren bool     `json:"has_children"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// handleCreateInternalIssue files a new issue that lives only in the hub store —
// no external tracker involved (ADR 0007). It allocates a repo-scoped
// ISSUE_PREFIX-N identifier, persists the issue with source "internal", and
// returns it so the board can show it immediately. The write is gated by the same
// exposure token as every other API request.
func (s *Server) handleCreateInternalIssue(w http.ResponseWriter, r *http.Request) {
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
	var req InternalIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}
	iss, err := s.stores.Issues().CreateInternal(repo.Root, s.internalPrefix(repo), draftFrom(req))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create issue: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toInternalIssueResponse(repo.Name, iss))
}

// handleInternalIssue reads (GET) or edits (PATCH) a single internal issue. Only
// internal issues are addressable here — a synced ticket's content is
// tracker-owned and never edited through the hub (ADR 0007).
func (s *Server) handleInternalIssue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getInternalIssue(w, r)
	case http.MethodPatch, http.MethodPut:
		s.updateInternalIssue(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) getInternalIssue(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	iss, found, err := s.stores.Issues().Internal(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read issue: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not an internal issue in this repo"})
		return
	}
	writeJSON(w, http.StatusOK, toInternalIssueResponse(repo.Name, iss))
}

func (s *Server) updateInternalIssue(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var req InternalIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}
	iss, err := s.stores.Issues().UpdateInternal(repo.Root, id, draftFrom(req))
	if errors.Is(err, hubstore.ErrInternalIssueNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not an internal issue in this repo"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update issue: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toInternalIssueResponse(repo.Name, iss))
}

// internalPrefix resolves the repo's internal-issue identifier prefix from its
// layered config, deriving from the repo name when ISSUE_PREFIX is unset. A config
// error degrades to the repo-name default rather than failing the create.
func (s *Server) internalPrefix(repo registry.Repo) string {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return config.InternalPrefix("", repo.Name)
	}
	return config.InternalPrefix(cfg.IssuePrefixConfigured, repo.Name)
}

func draftFrom(req InternalIssueRequest) hubstore.InternalDraft {
	return hubstore.InternalDraft{
		Title:       strings.TrimSpace(req.Title),
		Description: req.Description,
		State:       req.State,
		Labels:      cleanLabels(req.Labels),
		Parent:      strings.TrimSpace(req.Parent),
	}
}

func toInternalIssueResponse(repo string, iss hubstore.Issue) InternalIssueResponse {
	return InternalIssueResponse{
		Repo:        repo,
		ID:          iss.Identifier,
		Title:       iss.Title,
		Description: iss.Description,
		State:       iss.StatusGroup,
		Status:      iss.Status,
		Labels:      iss.Labels,
		Parent:      iss.Parent,
		Source:      iss.Source,
		HasChildren: iss.HasChildren,
		CreatedAt:   iss.CreatedAt,
		UpdatedAt:   iss.UpdatedAt,
	}
}
