package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// InternalIssueRequest is the body of POST /repos/{repo}/issues/internal and
// PATCH /repos/{repo}/issues/internal/{id}: the editable content of an internal
// issue — title, markdown description, workflow state (a status group like
// backlog|started|done), labels, an optional parent identifier nesting it under an
// epic, priority and ISO due date, and the same-repo identifiers blocking it or
// blocked by it. Relations are added, never replaced; DELETE on the relations
// endpoint drops them. Priority and due date are written only when the body names
// them, so an edit form that renders neither leaves both alone.
type InternalIssueRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent"`
	Priority    *int     `json:"priority"`
	DueDate     *string  `json:"due_date"`
	BlockedBy   []string `json:"blocked_by"`
	Blocks      []string `json:"blocks"`
}

// InternalIssueResponse is a stored internal issue as the create/edit forms and
// the board read it: its allocated identifier, content, normalized state and its
// display status, source, whether it heads an epic, and the blocking edges the
// store holds for it.
type InternalIssueResponse struct {
	Repo        string   `json:"repo"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Status      string   `json:"status"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent,omitempty"`
	Priority    int      `json:"priority"`
	DueDate     string   `json:"due_date,omitempty"`
	BlockedBy   []string `json:"blocked_by"`
	Blocks      []string `json:"blocks"`
	Source      string   `json:"source"`
	HasChildren bool     `json:"has_children"`
	Archived    bool     `json:"archived"`
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
	draft, err := draftFrom(req, hubstore.Issue{})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	iss, err := s.createInternalIssue(repo, draft)
	if writeRelationError(w, err) {
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.writeInternalIssue(w, http.StatusCreated, repo, iss)
}

// createInternalIssue is the write behind both the REST create route and the MCP
// create_ticket tool.
func (s *Server) createInternalIssue(repo registry.Repo, draft hubstore.InternalDraft) (hubstore.Issue, error) {
	iss, err := s.stores.Issues().CreateInternal(repo.Root, s.internalPrefix(repo), draft)
	if err != nil {
		return hubstore.Issue{}, fmt.Errorf("create issue: %w", err)
	}
	s.registerAttachments(repo.Root, iss.Identifier, scanIssueImages(iss.Description))
	s.bindUploadedAttachments(repo.Root, iss.Identifier, iss.Description)
	return iss, nil
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
	s.writeInternalIssue(w, http.StatusOK, repo, iss)
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
	issues := s.stores.Issues()
	stored, found, err := issues.Internal(repo.Root, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read issue: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not an internal issue in this repo"})
		return
	}
	draft, err := draftFrom(req, stored)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	iss, err := issues.UpdateInternal(repo.Root, id, draft)
	if errors.Is(err, hubstore.ErrInternalIssueNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not an internal issue in this repo"})
		return
	}
	if writeRelationError(w, err) {
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update issue: " + err.Error()})
		return
	}
	s.registerAttachments(repo.Root, iss.Identifier, scanIssueImages(iss.Description))
	s.bindUploadedAttachments(repo.Root, iss.Identifier, iss.Description)
	s.writeInternalIssue(w, http.StatusOK, repo, iss)
}

// scanIssueImages finds the images an internally-authored body embeds, so a
// pasted screenshot is listed the same way a synced ticket's files are. Only
// absolute URLs qualify: an uploaded image renders as a relative trau attachment
// route, which is already a stored row bound by the upload itself.
func scanIssueImages(description string) []tracker.Attachment {
	return tracker.AttachmentScanner{}.Scan(description)
}

// InternalTransitionRequest is the body of POST
// /repos/{repo}/issues/internal/{id}/transition: a loop-driven write to an
// internal issue. An empty state leaves the workflow state unchanged; add_labels
// and remove_labels apply case-insensitively; a non-empty comment is appended.
type InternalTransitionRequest struct {
	State        string   `json:"state"`
	AddLabels    []string `json:"add_labels"`
	RemoveLabels []string `json:"remove_labels"`
	Comment      string   `json:"comment"`
}

// handleInternalTransition applies a loop's status/label/comment write to an
// internal issue through the store (ADR 0007) — the seam the internal tracker
// provider uses so the loop drives internal issues without ever opening the
// database. Only source=internal issues are addressable; a synced or missing id
// answers 404.
func (s *Server) handleInternalTransition(w http.ResponseWriter, r *http.Request) {
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
	id := strings.TrimSpace(r.PathValue("id"))
	var req InternalTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	state := strings.TrimSpace(req.State)
	iss, err := s.stores.Issues().TransitionInternal(repo.Root, id, hubstore.InternalTransition{
		State:        state,
		AddLabels:    cleanLabels(req.AddLabels),
		RemoveLabels: cleanLabels(req.RemoveLabels),
		Comment:      req.Comment,
	})
	if errors.Is(err, hubstore.ErrInternalIssueNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not an internal issue in this repo"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "transition issue: " + err.Error()})
		return
	}
	if state != "" {
		s.retireClosedGrill(repo.Root)
	}
	s.writeInternalIssue(w, http.StatusOK, repo, iss)
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

// draftFrom builds the draft req asks the store to write, keeping stored's priority
// and due date for whichever of the two the request leaves unnamed — the store
// replaces every field at once, and a client that cannot show a field must not
// blank it.
func draftFrom(req InternalIssueRequest, stored hubstore.Issue) (hubstore.InternalDraft, error) {
	priority, due := stored.Priority, stored.DueDate
	if req.Priority != nil {
		priority = *req.Priority
	}
	if req.DueDate != nil {
		parsed, err := parseDueDate(*req.DueDate)
		if err != nil {
			return hubstore.InternalDraft{}, err
		}
		due = parsed
	}
	return hubstore.InternalDraft{
		Title:       strings.TrimSpace(req.Title),
		Description: req.Description,
		State:       req.State,
		Labels:      cleanLabels(req.Labels),
		Parent:      strings.TrimSpace(req.Parent),
		Priority:    priority,
		DueDate:     due,
		BlockedBy:   identifierList(req.BlockedBy),
		Blocks:      identifierList(req.Blocks),
	}, nil
}

// parseDueDate accepts an ISO calendar date, treating empty as no due date.
func parseDueDate(due string) (string, error) {
	due = strings.TrimSpace(due)
	if due == "" {
		return "", nil
	}
	if _, err := time.Parse(time.DateOnly, due); err != nil {
		return "", fmt.Errorf("due_date %q is not an ISO date (YYYY-MM-DD)", due)
	}
	return due, nil
}

// writeInternalIssue answers with iss plus the blocking edges the store holds for
// it, so a client reads back the graph it wrote.
func (s *Server) writeInternalIssue(w http.ResponseWriter, status int, repo registry.Repo, iss hubstore.Issue) {
	out := toInternalIssueResponse(repo.Name, iss)
	issues := s.stores.Issues()
	blockedBy, err := issues.Blockers(repo.Root, iss.Identifier)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read blockers: " + err.Error()})
		return
	}
	blocks, err := issues.Dependents(repo.Root, iss.Identifier)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read dependents: " + err.Error()})
		return
	}
	out.BlockedBy, out.Blocks = blockedBy, blocks
	writeJSON(w, status, out)
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
		Priority:    iss.Priority,
		DueDate:     iss.DueDate,
		BlockedBy:   []string{},
		Blocks:      []string{},
		Source:      iss.Source,
		HasChildren: iss.HasChildren,
		Archived:    iss.ArchivedAt != "",
		CreatedAt:   iss.CreatedAt,
		UpdatedAt:   iss.UpdatedAt,
	}
}
