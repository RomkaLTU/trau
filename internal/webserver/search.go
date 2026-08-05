package webserver

import (
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// SearchResult is one ranked issue match from the hub's local store — enough to
// render a result row and link to the ticket, without its description or comments.
type SearchResult struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Group       string   `json:"group"`
	Source      string   `json:"source"`
	Labels      []string `json:"labels"`
	Parent      string   `json:"parent,omitempty"`
	HasChildren bool     `json:"has_children"`
	URL         string   `json:"url,omitempty"`
}

// SearchResponse is the /repos/{repo}/issues/search resource: the query as run
// and its ranked matches, scoped to the one repo in the path.
type SearchResponse struct {
	Repo    string         `json:"repo"`
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

// handleIssueSearch runs a full-text query over the repo's stored issues —
// internal and synced alike — returning ranked matches from the hub's local
// index, no tracker round-trip. An empty or punctuation-only query answers 200
// with no results so a type-ahead never sees an error mid-keystroke.
func (s *Server) handleIssueSearch(w http.ResponseWriter, r *http.Request) {
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
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	matches, err := s.stores.Issues().Search(repo.Root, query, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search issues: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, SearchResponse{Repo: repo.Name, Query: query, Results: toSearchResults(matches)})
}

// A repo's share of a group is capped so one noisy repo cannot fill it.
const (
	globalSearchTypeLimit = 10
	globalSearchRepoLimit = 5
)

// GlobalIssueResult is one issue match tagged with the repo it was found in.
type GlobalIssueResult struct {
	SearchResult
	Repo string `json:"repo"`
}

// GlobalSettingResult is one config key match, carrying a display value that is
// already masked so a secret never leaves the hub.
type GlobalSettingResult struct {
	Repo  string `json:"repo"`
	Key   string `json:"key"`
	Group string `json:"group"`
	Value string `json:"value"`
	Kind  string `json:"kind,omitempty"`
}

// GlobalRunResult is one ledger run tagged with the repo it was found in.
type GlobalRunResult struct {
	RunView
	Repo string `json:"repo"`
}

// GlobalSearchGroups are the cross-repo matches by kind. Every group is present
// even when empty, so the palette renders one without a presence check.
type GlobalSearchGroups struct {
	Issues   []GlobalIssueResult   `json:"issues"`
	Settings []GlobalSettingResult `json:"settings"`
	Runs     []GlobalRunResult     `json:"runs"`
}

// GlobalSearchResponse is the /api/v1/search resource: the query as run and its
// repo-tagged matches.
type GlobalSearchResponse struct {
	Query   string             `json:"query"`
	Results GlobalSearchGroups `json:"results"`
}

// handleGlobalSearch fans one query out over every repo the hub knows, grouping
// issues, config keys, and ledger runs. Like handleIssueSearch it answers an
// empty or punctuation-only query with 200 and no matches, and a repo that
// fails to read is skipped rather than failing the whole response.
func (s *Server) handleGlobalSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	groups := GlobalSearchGroups{
		Issues:   []GlobalIssueResult{},
		Settings: []GlobalSettingResult{},
		Runs:     []GlobalRunResult{},
	}
	if terms := searchTerms(query); len(terms) > 0 {
		// The same list the repo switcher shows, so a repo registered from the web
		// is searchable before its first loop has ever run.
		for _, view := range s.repoViews() {
			groups.Issues = append(groups.Issues, s.searchRepoIssues(view.Repo, query)...)
			groups.Settings = appendCapped(groups.Settings, s.searchRepoSettings(view.Repo, terms))
			groups.Runs = appendCapped(groups.Runs, s.searchRepoRuns(view.Repo, terms))
		}
		groups.Issues = floatIdentifierMatches(groups.Issues, query)
	}
	writeJSON(w, http.StatusOK, GlobalSearchResponse{Query: query, Results: groups})
}

func (s *Server) searchRepoIssues(repo registry.Repo, query string) []GlobalIssueResult {
	matches, err := s.stores.Issues().Search(repo.Root, query, globalSearchRepoLimit)
	if err != nil {
		logger.Verbosef("global search issues %s: %v", repo.Name, err)
		return nil
	}
	results := toSearchResults(matches)
	out := make([]GlobalIssueResult, 0, len(results))
	for _, res := range results {
		out = append(out, GlobalIssueResult{SearchResult: res, Repo: repo.Name})
	}
	return out
}

func (s *Server) searchRepoSettings(repo registry.Repo, terms []string) []GlobalSettingResult {
	views, err := s.configViews(repo)
	if err != nil {
		logger.Verbosef("global search config %s: %v", repo.Name, err)
		return nil
	}
	out := make([]GlobalSettingResult, 0, globalSearchRepoLimit)
	for _, v := range views {
		if !matchesTerms(terms, v.Key, v.Group) {
			continue
		}
		out = append(out, GlobalSettingResult{
			Repo:  repo.Name,
			Key:   v.Key,
			Group: v.Group,
			Value: maskedConfigValue(v),
			Kind:  v.Kind,
		})
		if len(out) == globalSearchRepoLimit {
			break
		}
	}
	return out
}

func (s *Server) searchRepoRuns(repo registry.Repo, terms []string) []GlobalRunResult {
	out := []GlobalRunResult{}
	for _, run := range s.collectRuns(repo.Root) {
		if matchesTerms(terms, run.Ticket, run.Title) {
			out = append(out, GlobalRunResult{RunView: run, Repo: repo.Name})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

// maskedConfigValue renders a resolved key the way a result row reads it. The
// underlying view already withholds secret values, so no credential is in reach.
func maskedConfigValue(v ConfigKeyView) string {
	switch {
	case v.Secret && v.Set:
		return "••••••••"
	case v.Secret, v.Value == "":
		return "—"
	case v.Bool:
		if v.Value == "1" {
			return "on"
		}
		return "off"
	}
	return v.Value
}

// searchTerms splits free-form input into the alphanumeric runs a match has to
// cover, the same way the issue index tokenizes it.
func searchTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func matchesTerms(terms []string, fields ...string) bool {
	haystack := strings.ToLower(strings.Join(fields, " "))
	for _, t := range terms {
		if !strings.Contains(haystack, t) {
			return false
		}
	}
	return true
}

// floatIdentifierMatches reapplies the per-repo identifier pin across the merged
// group before capping it, so an exact match found in a repo late in registry
// order is neither buried nor cut.
func floatIdentifierMatches(matches []GlobalIssueResult, query string) []GlobalIssueResult {
	sort.SliceStable(matches, func(i, j int) bool {
		return identifierTier(matches[i].ID, query) < identifierTier(matches[j].ID, query)
	})
	return matches[:min(len(matches), globalSearchTypeLimit)]
}

func identifierTier(id, query string) int {
	switch {
	case strings.EqualFold(id, query):
		return 0
	case strings.HasPrefix(strings.ToLower(id), strings.ToLower(query)):
		return 1
	}
	return 2
}

func appendCapped[T any](group, matches []T) []T {
	if len(group) >= globalSearchTypeLimit {
		return group
	}
	group = append(group, matches[:min(len(matches), globalSearchRepoLimit)]...)
	return group[:min(len(group), globalSearchTypeLimit)]
}

func toSearchResults(issues []hubstore.Issue) []SearchResult {
	out := make([]SearchResult, 0, len(issues))
	for _, iss := range issues {
		labels := iss.Labels
		if labels == nil {
			labels = []string{}
		}
		out = append(out, SearchResult{
			ID:          iss.Identifier,
			Title:       iss.Title,
			Status:      iss.Status,
			Group:       iss.StatusGroup,
			Source:      iss.Source,
			Labels:      labels,
			Parent:      iss.Parent,
			HasChildren: iss.HasChildren,
			URL:         iss.URL,
		})
	}
	return out
}
