package webserver

import (
	"errors"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// BacklogEntry is one issue on the backlog board: its identifier, title, display
// status and normalized group, labels, source, epic relationship, and whether it
// carries the repo's ready label. Source distinguishes an internally-created issue
// from a synced tracker ticket (internal | linear | jira). Parent names the epic a
// sub-issue belongs to and is omitted for a top-level issue; HasChildren marks an
// epic.
type BacklogEntry struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Group       string   `json:"group"`
	Labels      []string `json:"labels"`
	Source      string   `json:"source"`
	Parent      string   `json:"parent,omitempty"`
	HasChildren bool     `json:"has_children"`
	Ready       bool     `json:"ready"`
}

// BacklogResponse is the full Project backlog for a repo, served from the hub's
// issue store — no live tracker call on the request path (ADR 0007). Provider is
// the repo's configured tracker; Freshness carries the store's last-synced and
// syncing state so the board can show synced-ness without blocking.
type BacklogResponse struct {
	Repo      string         `json:"repo"`
	Provider  string         `json:"provider"`
	Items     []BacklogEntry `json:"items"`
	Freshness *RepoFreshness `json:"freshness,omitempty"`
}

// handleBacklog lists a repo's full Project backlog — every ticket with its
// workflow status, not just the eligible queue — straight from the hub's issue
// store, so it answers instantly with no agent, no MCP, and no whole-team tracker
// walk on the request path. Internal issues appear alongside synced ones, tagged
// by source. When the store is stale it triggers a background sync
// (stale-while-revalidate) and reports the syncing/last-synced state on the
// response rather than blocking on the tracker.
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
	store := s.stores.Issues()
	items, err := store.Backlog(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list backlog: " + err.Error()})
		return
	}
	state, err := store.SyncState(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read sync state: " + err.Error()})
		return
	}
	s.syncer.refreshIfStale(repo.Root, state.LastSyncedAt)
	readyLabel, provider := s.backlogConfig(repo)
	writeJSON(w, http.StatusOK, BacklogResponse{
		Repo:      repo.Name,
		Provider:  provider,
		Items:     toBacklogEntries(items, readyLabel),
		Freshness: s.freshnessFrom(repo.Root, state),
	})
}

// backlogConfig resolves the repo's ready label and tracker provider from its
// layered config — the ready-label flag the board shows and the provider tag. It
// is a local file read, never a tracker call; a config error degrades to empty
// values so the board still serves from the store.
func (s *Server) backlogConfig(repo registry.Repo) (readyLabel, provider string) {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return "", ""
	}
	return cfg.ReadyLabel, cfg.TrackerProvider
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

// toBacklogEntries maps the stored issues onto the JSON board rows, deriving the
// ready flag from each issue's labels against the repo's ready label. Stored
// labels are always a decoded slice, so the board never sees null.
func toBacklogEntries(issues []hubstore.Issue, readyLabel string) []BacklogEntry {
	out := make([]BacklogEntry, 0, len(issues))
	for _, iss := range issues {
		out = append(out, BacklogEntry{
			ID:          iss.Identifier,
			Title:       iss.Title,
			Status:      iss.Status,
			Group:       iss.StatusGroup,
			Labels:      iss.Labels,
			Source:      iss.Source,
			Parent:      iss.Parent,
			HasChildren: iss.HasChildren,
			Ready:       hasLabel(iss.Labels, readyLabel),
		})
	}
	return out
}

// hasLabel reports whether labels carries want (case-insensitively), so the board
// can flag ready-labelled tickets. An empty want is never a match.
func hasLabel(labels []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), want) {
			return true
		}
	}
	return false
}
