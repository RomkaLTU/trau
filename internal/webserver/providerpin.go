package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
)

// ProviderPinRequest is the body of PUT /repos/{repo}/issues/{id}/provider: the
// Provider every run of the ticket uses. An empty value clears the pin, so the
// next run falls back to the repo default.
type ProviderPinRequest struct {
	Provider string `json:"provider"`
}

// handleIssueProviderPin pins the Provider a ticket's runs use, for an issue of any
// source. The pin is hub-local, so unlike the assignee it needs no tracker writer and
// works on internal issues too. An unregistered name is refused rather than stored
// for a later run to trip over.
func (s *Server) handleIssueProviderPin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	var req ProviderPinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	reg := agent.DefaultRegistry()
	provider := strings.TrimSpace(req.Provider)
	if _, known := reg.Lookup(provider); provider != "" && !known {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unknown provider %q (expected: %s)", provider, strings.Join(reg.Names(), " | ")),
		})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	iss, found, err := s.stores.Issues().SetProvider(repo.Root, id, provider)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "pin provider: " + err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": id + " is not an issue in this repo"})
		return
	}
	writeJSON(w, http.StatusOK, s.storeIssueResponse(repo, iss))
}
