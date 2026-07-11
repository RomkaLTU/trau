package webserver

import (
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
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
