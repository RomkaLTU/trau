package webserver

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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
// epic. ChildrenSettled/ChildrenTotal report the epic's settled (done + canceled)
// and total sub-issue counts over all children in the store, and are present
// only on an epic row so the board can show its progress without a second call.
type BacklogEntry struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Status          string   `json:"status"`
	Group           string   `json:"group"`
	Labels          []string `json:"labels"`
	Source          string   `json:"source"`
	Parent          string   `json:"parent,omitempty"`
	HasChildren     bool     `json:"has_children"`
	ChildrenSettled *int     `json:"children_settled,omitempty"`
	ChildrenTotal   *int     `json:"children_total,omitempty"`
	Ready           bool     `json:"ready"`
}

// BacklogResponse is a repo's Project backlog served from the hub's issue store —
// no live tracker call on the request path (ADR 0007). Provider is the repo's
// configured tracker; Items is the requested page of matches; Total is the number
// of matches before pagination so the board can page; Counts is the per-status-group
// match totals with the state filter ignored, so the board's section headers and
// hidden-count hint hold whichever groups are on screen; Freshness carries the
// store's last-synced and syncing state so the board can show synced-ness without
// blocking.
type BacklogResponse struct {
	Repo      string         `json:"repo"`
	Provider  string         `json:"provider"`
	Items     []BacklogEntry `json:"items"`
	Total     int            `json:"total"`
	Counts    map[string]int `json:"counts"`
	Freshness *RepoFreshness `json:"freshness,omitempty"`
}

// maxBacklogLimit caps a single backlog page so a caller cannot ask the store for
// an unbounded slab; a request without a limit is still served in full (Limit 0).
const maxBacklogLimit = 500

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
	items, total, counts, err := store.BacklogPage(repo.Root, backlogFilter(r.URL.Query()))
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
		Total:     total,
		Counts:    counts,
		Freshness: s.freshnessFrom(repo.Root, state),
	})
}

// LabelFacet is one entry on the board's label combobox: a distinct label name
// carried by the repo's stored issues and how many of them carry it.
type LabelFacet struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// LabelsResponse is a repo's distinct issue labels with counts, served from the
// hub's issue store.
type LabelsResponse struct {
	Repo   string       `json:"repo"`
	Labels []LabelFacet `json:"labels"`
}

// handleLabels serves the facet the board's label combobox filters on, straight
// from the hub's issue store with no tracker call on the request path (ADR 0007).
func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
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
	labels, err := s.stores.Issues().Labels(repo.Root)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list labels: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, LabelsResponse{
		Repo:   repo.Name,
		Labels: toLabelFacets(labels),
	})
}

// toLabelFacets maps the store's label counts onto the JSON facet rows.
func toLabelFacets(labels []hubstore.LabelCount) []LabelFacet {
	out := make([]LabelFacet, 0, len(labels))
	for _, l := range labels {
		out = append(out, LabelFacet{Name: l.Name, Count: l.Count})
	}
	return out
}

// backlogFilter reads the board's filter and pagination controls off the query
// string: state (one or more workflow state groups, comma-separated and/or
// repeated), label, source (internal | synced), q (substring text match), and
// limit/offset. Absent or malformed values fall back to the zero filter, so a
// bare request is the unfiltered board.
func backlogFilter(q url.Values) hubstore.BacklogFilter {
	return hubstore.BacklogFilter{
		Groups: stateGroups(q["state"]),
		Label:  strings.TrimSpace(q.Get("label")),
		Source: strings.TrimSpace(q.Get("source")),
		Text:   strings.TrimSpace(q.Get("q")),
		Limit:  backlogLimit(q.Get("limit")),
		Offset: backlogOffset(q.Get("offset")),
	}
}

// stateGroups flattens the repeated and/or comma-separated state params into the
// distinct status groups to union, dropping blanks so state=&state=started reads
// as just started and an absent state means every group.
func stateGroups(values []string) []string {
	groups := []string{}
	seen := map[string]struct{}{}
	for _, v := range values {
		for _, g := range strings.Split(v, ",") {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if _, dup := seen[g]; dup {
				continue
			}
			seen[g] = struct{}{}
			groups = append(groups, g)
		}
	}
	return groups
}

// backlogLimit parses a page size, clamped to maxBacklogLimit. An absent or
// non-positive value yields 0, which the store reads as "no limit".
func backlogLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	if n > maxBacklogLimit {
		return maxBacklogLimit
	}
	return n
}

// backlogOffset parses a page offset; an absent or negative value yields 0.
func backlogOffset(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
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

// trackerResolution is the outcome of resolving a repo's tracker for a hub-side
// sync: the reader and the provider that answered, plus the config signals that
// turn a downstream binding or pull failure into an actionable error when the
// provider had to be inferred from credentials rather than set explicitly.
type trackerResolution struct {
	provider  string
	reader    tracker.Reader
	explicit  bool
	jiraCreds bool
}

// resolveReader resolves the repo's layered config, settles the effective tracker
// provider (inferring jira when the project layer carries Jira credentials but no
// TRACKER_PROVIDER is set), and builds a direct Reader for it.
func (s *Server) resolveReader(repo registry.Repo) (trackerResolution, error) {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, sources, err := config.LoadLayeredWithSources(projectPath, userPath, "", "")
	if err != nil {
		return trackerResolution{}, err
	}
	cfg.TrackerProvider = cfg.ResolveSyncProvider(sources)
	reader, err := s.newReader(cfg)
	return trackerResolution{
		provider:  cfg.TrackerProvider,
		reader:    reader,
		explicit:  cfg.TrackerProviderExplicit(sources),
		jiraCreds: cfg.HasJiraCredentials(),
	}, err
}

// actionableErr rewrites a binding or pull failure into one that names what to fix
// when the tracker provider was inferred rather than set. An explicit provider — or
// a failure the resolution cannot explain — is returned unchanged, so a repo that
// names its tracker still surfaces the tracker's own error.
func (r trackerResolution) actionableErr(err error) error {
	if err == nil || r.explicit {
		return err
	}
	switch r.provider {
	case "jira":
		if errors.Is(err, tracker.ErrReaderUnavailable) {
			return fmt.Errorf("inferred jira from project-layer credentials but no Jira project key is set — set PROJECT: %w", err)
		}
	case "linear":
		if r.jiraCreds {
			return fmt.Errorf("repo has Jira credentials but TRACKER_PROVIDER is unset — set TRACKER_PROVIDER=jira (tried linear: %v)", err)
		}
	}
	return err
}

// readerFor resolves the repo's tracker and returns the provider name and Reader,
// so a caller that only labels the answering tracker need not carry the full
// resolution.
func (s *Server) readerFor(repo registry.Repo) (string, tracker.Reader, error) {
	res, err := s.resolveReader(repo)
	return res.provider, res.reader, err
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
// mapping the provider's credentials the same way the loop's tracker is wired. It
// resolves the provider through EffectiveTrackerProvider so a repo with no external
// tracker configured reports no credentials rather than reaching for a linear
// reader it cannot build.
func defaultReader(cfg config.Config) (tracker.Reader, error) {
	provider := cfg.EffectiveTrackerProvider()
	if provider == "internal" {
		return nil, tracker.ErrReaderUnavailable
	}
	tc := tracker.Config{
		Team:            cfg.LinearTeam,
		Project:         cfg.Project,
		ReadyLabel:      cfg.ReadyLabel,
		QuarantineLabel: cfg.QuarantineLabel,
		SplitLabel:      cfg.SplitLabel,
		APIKey:          cfg.LinearAPIKey,
	}
	if provider == "jira" {
		tc.APIKey = cfg.JiraAPIToken
		tc.BaseURL = cfg.JiraBaseURL
		tc.Email = cfg.JiraEmail
	}
	return tracker.NewReader(provider, tc)
}

// toBacklogEntries maps the stored issues onto the JSON board rows, deriving the
// ready flag from each issue's labels against the repo's ready label. Stored
// labels are always a decoded slice, so the board never sees null.
func toBacklogEntries(issues []hubstore.Issue, readyLabel string) []BacklogEntry {
	out := make([]BacklogEntry, 0, len(issues))
	for _, iss := range issues {
		entry := BacklogEntry{
			ID:          iss.Identifier,
			Title:       iss.Title,
			Status:      iss.Status,
			Group:       iss.StatusGroup,
			Labels:      iss.Labels,
			Source:      iss.Source,
			Parent:      iss.Parent,
			HasChildren: iss.HasChildren,
			Ready:       hasLabel(iss.Labels, readyLabel),
		}
		if iss.HasChildren {
			settled, total := iss.ChildrenSettled, iss.ChildrenTotal
			entry.ChildrenSettled = &settled
			entry.ChildrenTotal = &total
		}
		out = append(out, entry)
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
