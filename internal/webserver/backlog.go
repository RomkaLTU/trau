package webserver

import (
	"errors"
	"net/http"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// BacklogEntry is one issue on the backlog board: its identifier, title, display
// status and normalized group, labels, epic relationship, and whether it carries
// the repo's ready label. Parent names the epic a sub-issue belongs to and is
// omitted for a top-level issue; HasChildren marks an epic.
type BacklogEntry struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Group       string   `json:"group"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent,omitempty"`
	HasChildren bool     `json:"has_children"`
	Ready       bool     `json:"ready"`
}

// BacklogResponse is the full Project backlog for a repo, listed directly through
// the tracker's own API, plus the provider that answered.
type BacklogResponse struct {
	Repo     string         `json:"repo"`
	Provider string         `json:"provider"`
	Items    []BacklogEntry `json:"items"`
}

// handleBacklog lists a repo's full Project backlog — every ticket with its
// workflow status, not just the eligible queue — directly through its tracker's
// REST/GraphQL API, no agent and no MCP. A repo with no direct tracker
// credentials answers 422 so the board shows a backlog-unavailable state rather
// than an error page.
func (s *Server) handleBacklog(w http.ResponseWriter, r *http.Request) {
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
	provider, reader, err := s.readerFor(repo)
	if err != nil {
		writeReaderErr(w, err)
		return
	}
	items, err := reader.Backlog(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "list backlog: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, BacklogResponse{Repo: repo.Name, Provider: provider, Items: toBacklogEntries(items)})
}

// readerFor resolves the repo's layered config and builds a direct tracker Reader
// from it, returning the provider name alongside so the board can label which
// tracker answered.
func (s *Server) readerFor(repo registry.Repo) (string, tracker.Reader, error) {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return "", nil, err
	}
	reader, err := s.newReader(cfg)
	return cfg.TrackerProvider, reader, err
}

// writeReaderErr maps a Reader build failure to a response. A repo with no direct
// tracker credentials cannot browse its backlog over the hub — it is a config
// state, not a bad request — so it answers 422 with a hint the board renders as a
// backlog-unavailable state.
func writeReaderErr(w http.ResponseWriter, err error) {
	if errors.Is(err, tracker.ErrReaderUnavailable) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "this repo has no direct tracker credentials configured; set LINEAR_API_KEY, or the full Jira REST credentials (JIRA_BASE_URL, JIRA_EMAIL, JIRA_API_TOKEN)",
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "tracker unavailable: " + err.Error()})
}

// defaultReader builds a direct tracker Reader from a repo's resolved config,
// mapping the provider's credentials the same way the loop's tracker is wired.
func defaultReader(cfg config.Config) (tracker.Reader, error) {
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
	return tracker.NewReader(cfg.TrackerProvider, tc)
}

// toBacklogEntries maps the tracker's provider-neutral items onto the JSON board
// rows, normalizing nil label slices to an empty array so the board never sees
// null.
func toBacklogEntries(items []tracker.BacklogItem) []BacklogEntry {
	out := make([]BacklogEntry, 0, len(items))
	for _, it := range items {
		labels := it.Labels
		if labels == nil {
			labels = []string{}
		}
		out = append(out, BacklogEntry{
			ID:          it.ID,
			Title:       it.Title,
			Status:      it.Status,
			Group:       string(it.Group),
			Labels:      labels,
			Parent:      it.Parent,
			HasChildren: it.HasChildren,
			Ready:       it.Ready,
		})
	}
	return out
}
