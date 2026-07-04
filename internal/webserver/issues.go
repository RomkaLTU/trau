package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// CreateIssueRequest is the body of POST /repos/{repo}/issues: a title, an
// optional markdown description, and any labels to apply (e.g. the ready label).
type CreateIssueRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

// CreatedIssue is returned when the hub files a new issue: the tracker's own
// identifier and a link to it, plus the provider that created it.
type CreatedIssue struct {
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
	Provider   string `json:"provider"`
}

// CommentRequest is the body of POST /repos/{repo}/runs/{ticket}/comment.
type CommentRequest struct {
	Body string `json:"body"`
}

// handleCreateIssue files a new issue in the repo's configured tracker directly
// through its REST/GraphQL API — no agent, no MCP. The write is gated by the same
// exposure token as every other API request.
func (s *Server) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
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
	var req CreateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}
	provider, writer, err := s.writerFor(repo)
	if err != nil {
		writeWriterErr(w, err)
		return
	}
	issue, err := writer.CreateIssue(r.Context(), tracker.IssueDraft{
		Title:       title,
		Description: req.Description,
		Labels:      cleanLabels(req.Labels),
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "create issue: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, CreatedIssue{Identifier: issue.Identifier, URL: issue.URL, Provider: provider})
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
	if !runExists(repo.RunsDir, ticket) {
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
		Team:            cfg.LinearTeam,
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
