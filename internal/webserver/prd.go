package webserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker"
)

// PublishPRDRequest is the body of POST /repos/{repo}/prd: a PRD title and its
// markdown body to publish to the repo's configured tracker.
type PublishPRDRequest struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

// PublishedPRD is returned when the hub publishes a PRD: a link to it, the
// tracker that stored it, how it was stored (a Linear document or a Jira issue),
// and the issue identifier when the Jira fallback filed one.
type PublishedPRD struct {
	URL        string `json:"url"`
	Identifier string `json:"identifier,omitempty"`
	Kind       string `json:"kind"`
	Provider   string `json:"provider"`
}

// handlePublishPRD publishes a markdown PRD to the repo's configured tracker
// directly through its REST/GraphQL API: a Linear project document, or — for a
// Jira-configured repo — an issue whose description carries the PRD. The markdown
// is sent verbatim so the published content matches the draft. Gated by the same
// exposure token as every other write.
func (s *Server) handlePublishPRD(w http.ResponseWriter, r *http.Request) {
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
	var req PublishPRDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}
	if strings.TrimSpace(req.Markdown) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "markdown body is required"})
		return
	}
	provider, writer, err := s.writerFor(repo)
	if err != nil {
		writeWriterErr(w, err)
		return
	}
	doc, err := writer.PublishDocument(r.Context(), tracker.DocumentDraft{Title: title, Markdown: req.Markdown})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "publish prd: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, PublishedPRD{
		URL:        doc.URL,
		Identifier: doc.Identifier,
		Kind:       doc.Kind,
		Provider:   provider,
	})
}
