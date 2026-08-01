package webserver

import (
	"net/http"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
)

// AzureFeature is one Feature a created work item can hang off.
type AzureFeature struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// AzureCreateOptions is what an Azure DevOps create picks from: the
// requirement-level work-item types the project declares, its own default first,
// and the Features already on the repo's board.
type AzureCreateOptions struct {
	Types    []string       `json:"types"`
	Features []AzureFeature `json:"features"`
}

// handleAzureCreateOptions serves the hierarchy choices an inbox create offers on
// an Azure DevOps repo. The types come from the project's backlog configuration —
// trau files at requirement level and never above it, so an Epic or a Feature is
// never on the list. The Features come from the hub's synced mirror rather than a
// live WIQL query: the mirror already covers exactly the slice of the board the
// loop picks from, so the picker cannot offer a Feature the repo does not mirror,
// and it costs no PAT budget (ADR 0031).
func (s *Server) handleAzureCreateOptions(w http.ResponseWriter, r *http.Request) {
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
	_, writer, err := s.grillWriterFor(repo, "")
	if err != nil {
		writeWriterErr(w, err)
		return
	}
	typer, ok := writer.(tracker.IssueTyper)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this repo's tracker has no typed work-item hierarchy",
		})
		return
	}
	types, err := typer.CreatableTypes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "read work-item types: " + err.Error()})
		return
	}
	features, err := s.stores.Issues().AtLevel(repo.Root, string(azureapi.LevelFeature))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list features: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, AzureCreateOptions{Types: types, Features: toAzureFeatures(features)})
}

func toAzureFeatures(issues []hubstore.Issue) []AzureFeature {
	out := make([]AzureFeature, 0, len(issues))
	for _, iss := range issues {
		out = append(out, AzureFeature{ID: iss.Identifier, Title: iss.Title})
	}
	return out
}
