package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// CommentRequest is the body of POST /repos/{repo}/runs/{ticket}/comment.
type CommentRequest struct {
	Body string `json:"body"`
}

// IssueResponse is the /repos/{repo}/issues/{id} resource: one ticket read from
// the hub's issue store so the run-once form and the pipeline can read its content
// without a tracker call (ADR 0007). Group is the normalized status bucket the form
// uses to warn about an unusual status (already done, in progress); Description and
// Comments carry the content prompt-building injects for a synced ticket.
type IssueResponse struct {
	Repo     string `json:"repo"`
	Provider string `json:"provider"`
	// ProviderPin is the Provider pinned on the ticket itself, empty when its runs
	// fall back to the repo default. Unrelated to Provider above, which names the
	// repo's tracker.
	ProviderPin string        `json:"provider_pin"`
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      string        `json:"status"`
	Group       string        `json:"group"`
	Labels      []string      `json:"labels"`
	Assignee    *AssigneeInfo `json:"assignee"`
	Ready       bool          `json:"ready"`
	Parent      string        `json:"parent,omitempty"`
	Source      string        `json:"source,omitempty"`
	URL         string        `json:"url,omitempty"`
	CreatedAt   string        `json:"created_at,omitempty"`
	HasChildren bool          `json:"has_children"`
	// Children counts the sub-issues a purge of this ticket would take with it —
	// every child row, archived ones included — so a delete confirm can name the
	// whole blast radius. Zero on a leaf.
	Children int            `json:"children"`
	Blockers []string       `json:"blockers,omitempty"`
	Blocked  bool           `json:"blocked,omitempty"`
	Comments []IssueComment `json:"comments"`
	// Project is the ticket's own tracker project; InProject reports whether it
	// matches the repo's configured project, so a cross-project ticket can be
	// shown but refused rather than launched into the wrong repo.
	Project   string `json:"project,omitempty"`
	InProject bool   `json:"in_project"`
	Deleted   bool   `json:"deleted,omitempty"`
	Archived  bool   `json:"archived"`
}

// IssueComment is one comment on an issue as the store returns it.
type IssueComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at,omitempty"`
}

// SyncedMirrorRequest is the body of POST /repos/{repo}/issues/{id}: a tracker
// write's status/label change to mirror onto a synced issue's store row so the
// board never lags a transition (ADR 0007).
type SyncedMirrorRequest struct {
	Status       string   `json:"status"`
	StatusGroup  string   `json:"status_group"`
	AddLabels    []string `json:"add_labels"`
	RemoveLabels []string `json:"remove_labels"`
}

// handleIssue reads a single ticket by identifier (GET), mirrors a tracker write
// onto its synced store row (POST), or hard-deletes it (DELETE). All three answer
// from the hub's issue store — no agent, no MCP on the request path (ADR 0007).
func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getIssue(w, r)
	case http.MethodPost:
		s.mirrorIssue(w, r)
	case http.MethodDelete:
		s.deleteIssue(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// DeleteIssueResponse names every identifier the purge removed, so the UI can
// report what went: the ticket alone for a leaf, the epic and its children for a
// family.
type DeleteIssueResponse struct {
	Deleted []string `json:"deleted"`
}

// deleteIssue hard-deletes a stored issue and, when it heads an epic, its
// children — see Issues.Purge for what that takes down and what it leaves — then
// hands the family's git footprint to purgeGitFootprint. A family member mid-run
// answers 409 and changes nothing; an unknown repo or identifier answers 404.
func (s *Server) deleteIssue(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	res, found, err := s.stores.Issues().Purge(repo, id)
	var running *hubstore.IssueRunningError
	switch {
	case errors.As(err, &running):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": running.Identifier + " is running — stop the run before deleting",
		})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete issue: " + err.Error()})
		return
	case !found:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " not found in this repo"})
		return
	}
	if err := s.stores.Attachments().CollectOrphans(res.OrphanedBlobs); err != nil {
		logger.Verbosef("delete %s: collect attachment blobs: %v", id, err)
	}
	s.purgeGitFootprint(repo, res.Deleted)
	writeJSON(w, http.StatusOK, DeleteIssueResponse{Deleted: res.Deleted})
}

// getIssue serves a ticket store-first: a stored issue (synced or internal) is
// returned with its content and comments; a miss falls back to a one-off tracker
// fetch — syncing an in-project ticket into the store first so later reads stay
// local, and returning a cross-project ticket with InProject false so the caller
// refuses it rather than running it here (ADR 0007). An unknown identifier answers
// 404 so the form shows a not-found state.
func (s *Server) getIssue(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket id is required"})
		return
	}
	store := s.stores.Issues()
	switch iss, found, err := store.Find(repo.Root, id); {
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read issue: " + err.Error()})
		return
	case found && iss.DeletedAt == "":
		writeJSON(w, http.StatusOK, s.storeIssueResponse(repo, iss))
		return
	}

	provider, reader, err := s.readerFor(repo)
	if err != nil {
		// A synced repo with no direct credentials keeps the 422 credential hint the
		// run-once form shows; any other build failure — an internal-only or github
		// repo has no store fallback — means the id simply isn't resolvable here.
		if errors.Is(err, tracker.ErrReaderUnavailable) {
			writeReaderErr(w, err)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " not found in this repo's store"})
		return
	}
	item, err := reader.Issue(r.Context(), id)
	if err != nil {
		if errors.Is(err, tracker.ErrIssueNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " not found in this repo's tracker"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "fetch issue: " + err.Error()})
		return
	}
	if !item.InProject {
		writeJSON(w, http.StatusOK, summaryIssueResponse(repo.Name, provider, item))
		return
	}
	if _, serr := s.syncRepo(r.Context(), repo); serr != nil {
		logger.Verbosef("issue %s: sync-in failed: %v", id, serr)
	}
	if iss, found, ferr := store.Find(repo.Root, id); ferr == nil && found && iss.DeletedAt == "" {
		writeJSON(w, http.StatusOK, s.storeIssueResponse(repo, iss))
		return
	}
	writeJSON(w, http.StatusOK, summaryIssueResponse(repo.Name, provider, item))
}

// mirrorIssue applies a tracker write's status/label change to a synced issue's
// store row, so a transition trau performed against the tracker lands in the board
// in the same motion (ADR 0007). Only synced rows are addressable; a missing or
// internal identifier answers 404.
func (s *Server) mirrorIssue(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var req SyncedMirrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	iss, found, err := s.stores.Issues().UpdateSynced(repo.Root, id, hubstore.SyncedPatch{
		Status:       strings.TrimSpace(req.Status),
		StatusGroup:  strings.TrimSpace(req.StatusGroup),
		AddLabels:    cleanLabels(req.AddLabels),
		RemoveLabels: cleanLabels(req.RemoveLabels),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mirror issue: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not a synced issue in this repo"})
		return
	}
	writeJSON(w, http.StatusOK, s.storeIssueResponse(repo, iss))
}

// storeIssueResponse maps a stored issue onto the API resource, deriving the ready
// flag and provider from the repo's layered config (a local read, never a tracker
// call). A stored issue is by construction in the repo's Project, so InProject is
// true.
func (s *Server) storeIssueResponse(repo registry.Repo, iss hubstore.Issue) IssueResponse {
	readyLabel, provider := s.backlogConfig(repo)
	labels := iss.Labels
	if labels == nil {
		labels = []string{}
	}
	return IssueResponse{
		Repo:        repo.Name,
		Provider:    provider,
		ProviderPin: iss.Provider,
		ID:          iss.Identifier,
		Title:       iss.Title,
		Description: iss.Description,
		Status:      iss.Status,
		Group:       iss.StatusGroup,
		Labels:      labels,
		Assignee:    assigneeInfo(iss, s.repoMeID(repo.Root)),
		Ready:       hasLabel(labels, readyLabel),
		Parent:      iss.Parent,
		Source:      iss.Source,
		URL:         iss.URL,
		CreatedAt:   iss.CreatedAt,
		HasChildren: iss.HasChildren,
		Children:    s.childCount(repo.Root, iss),
		Blockers:    iss.Blockers,
		Blocked:     iss.Blocked,
		Comments:    toIssueComments(iss.Comments),
		InProject:   true,
		Deleted:     iss.DeletedAt != "",
		Archived:    iss.ArchivedAt != "",
	}
}

// childCount reports how many children a purge of iss would remove, following the
// same has_children short-circuit purgeFamily takes, so the number a delete
// confirm shows is the number the purge deletes.
func (s *Server) childCount(root string, iss hubstore.Issue) int {
	if !iss.HasChildren {
		return 0
	}
	n, err := s.stores.Issues().ChildCount(root, iss.Identifier)
	if err != nil {
		logger.Verbosef("issue %s: count children: %v", iss.Identifier, err)
	}
	return n
}

// repoMeID returns the repo binding's resolved Me identity id, used to flag an
// assignee as Me, or "" when it is unresolved or unreadable — a best-effort
// annotation that never fails the response it decorates.
func (s *Server) repoMeID(root string) string {
	state, err := s.stores.Issues().SyncState(root)
	if err != nil {
		logger.Verbosef("issue: read sync identity: %v", err)
		return ""
	}
	return state.Me.ID
}

// summaryIssueResponse maps a tracker-fetched issue summary onto the API resource —
// the fallback for a ticket not yet in the store, carrying the Project/InProject
// ownership signal but no description or comments (the summary read has neither).
func summaryIssueResponse(repo, provider string, item tracker.IssueSummary) IssueResponse {
	labels := item.Labels
	if labels == nil {
		labels = []string{}
	}
	return IssueResponse{
		Repo:      repo,
		Provider:  provider,
		ID:        item.ID,
		Title:     item.Title,
		Status:    item.Status,
		Group:     string(item.Group),
		Labels:    labels,
		Ready:     item.Ready,
		Parent:    item.Parent,
		Comments:  []IssueComment{},
		Project:   item.Project,
		InProject: item.InProject,
	}
}

func toIssueComments(cs []hubstore.Comment) []IssueComment {
	out := make([]IssueComment, 0, len(cs))
	for _, c := range cs {
		out = append(out, IssueComment{Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	return out
}

// handleRunComment adds a comment to the run's existing ticket, so a follow-up
// observed from a run's detail view lands on the same ticket in the tracker.
func (s *Server) handleRunComment(w http.ResponseWriter, r *http.Request) {
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
	ticket := r.PathValue("ticket")
	s.importCheckpoints(repo)
	if _, found, _ := s.stores.Checkpoints().One(repo.Root, ticket); !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown run"})
		return
	}
	var req CommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "comment body is required"})
		return
	}
	_, writer, err := s.writerFor(repo)
	if err != nil {
		writeWriterErr(w, err)
		return
	}
	if err := writer.AddComment(r.Context(), ticket, body); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "add comment: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ok", "ticket": ticket})
}

// writerFor resolves the repo's layered config and builds a direct tracker
// Writer from it, returning the provider name alongside so a created issue can be
// labelled with the tracker that produced it.
func (s *Server) writerFor(repo registry.Repo) (string, tracker.Writer, error) {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return "", nil, err
	}
	writer, err := s.newWriter(cfg)
	return cfg.TrackerProvider, writer, err
}

// writeWriterErr maps a Writer build failure to a response. A repo with no direct
// tracker credentials cannot create work over the hub — it is a config state, not
// a bad request — so it answers 422 with a hint rather than a generic 500.
func writeWriterErr(w http.ResponseWriter, err error) {
	if errors.Is(err, tracker.ErrWriterUnavailable) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "this repo has no direct tracker credentials configured; set LINEAR_API_KEY, or the full Jira REST credentials (JIRA_BASE_URL, JIRA_EMAIL, JIRA_API_TOKEN)",
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "tracker unavailable: " + err.Error()})
}

// defaultWriter builds a direct tracker Writer from a repo's resolved config,
// mapping the provider's credentials the same way the loop's tracker is wired.
func defaultWriter(cfg config.Config) (tracker.Writer, error) {
	tc := tracker.Config{
		Team:            cfg.TrackerKey(),
		Project:         cfg.Project,
		ReadyLabel:      cfg.ReadyLabel,
		QuarantineLabel: cfg.QuarantineLabel,
		SplitLabel:      cfg.SplitLabel,
		APIKey:          cfg.LinearAPIKey,
	}
	if cfg.TrackerProvider == "jira" {
		tc.APIKey = cfg.JiraAPIToken
		tc.BaseURL = cfg.JiraBaseURL
		tc.Email = cfg.JiraEmail
		tc.EpicType = cfg.JiraEpicType
	}
	return tracker.NewWriter(cfg.TrackerProvider, tc)
}

// cleanLabels trims each label and drops the blanks, so a trailing comma or a
// stray space in the form never sends an empty label to the tracker.
func cleanLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
