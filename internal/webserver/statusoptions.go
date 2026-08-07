package webserver

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// statusOptionsTimeout bounds the tracker round trips one status-options read
// makes. It reads a backlog configuration, a work-item type's states, and every
// board of every team the repo names, so it is allowed noticeably longer than a
// single work-item call — but never long enough to hold a settings page open.
const statusOptionsTimeout = 45 * time.Second

// StatusColumn is one groupable column the mapping editor lists: the name the
// grammar keys on, and the group the provider's own metadata suggests for it.
type StatusColumn struct {
	Name           string `json:"name"`
	SuggestedGroup string `json:"suggestedGroup"`
}

// StatusPinOption is one workflow status a STATUS_* pin may name, with the
// process-agnostic category the provider files it under so the editor can show
// what a state means under a renamed workflow.
type StatusPinOption struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

// StatusOptionsResponse is what the settings status-mapping editor renders. The
// shape is provider-agnostic on purpose: a provider that gains a mapping editor
// later fills the same two lists rather than adding a surface of its own.
//
// A credential or network failure keeps the 200 and carries Error and Hint
// instead — the same remediation-friendly shape the tracker connection test
// answers with — so the editor can degrade to its config-only fallback rather
// than treating a lapsed PAT as a missing feature.
type StatusOptionsResponse struct {
	Provider   string            `json:"provider"`
	Grouping   []StatusColumn    `json:"grouping"`
	PinOptions []StatusPinOption `json:"pinOptions"`
	Error      string            `json:"error,omitempty"`
	Hint       string            `json:"hint,omitempty"`
}

// handleTrackerStatusOptions serves the choices a repo's status mapping is made
// of. Only Azure DevOps has both halves today — a board whose columns are the
// grouping key and a workflow whose states the pins name — so every other
// provider answers 404 and the settings page keeps its generic rows.
func (s *Server) handleTrackerStatusOptions(w http.ResponseWriter, r *http.Request) {
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
	cfg, _, err := s.resolveRepoConfig(repo)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read config: " + err.Error()})
		return
	}
	provider := cfg.EffectiveTrackerProvider()
	if provider != "azure" {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "this repo's tracker has no status-mapping options",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), statusOptionsTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, azureStatusOptions(ctx, provider, cfg))
}

// azureStatusOptions reads the board columns and workflow states of an Azure
// DevOps repo, shaping a failure into the response rather than an error status:
// the editor still has the repo's own mapping to fall back on, and an operator
// with a lapsed PAT needs the hint more than the status code.
func azureStatusOptions(ctx context.Context, provider string, cfg config.Config) StatusOptionsResponse {
	out := StatusOptionsResponse{
		Provider:   provider,
		Grouping:   []StatusColumn{},
		PinOptions: []StatusPinOption{},
	}
	if missing := missingCredentials(provider, cfg); len(missing) > 0 {
		out.Error = "missing " + strings.Join(missing, ", ")
		out.Hint = enterCredentialsHint(provider)
		return out
	}
	opts, err := tracker.AzureStatusOptions(ctx, readerConfig(cfg, provider))
	if err != nil {
		out.Error = err.Error()
		out.Hint = connHint(provider, cfg, err)
		return out
	}
	for _, col := range opts.Columns {
		out.Grouping = append(out.Grouping, StatusColumn{
			Name:           col.Name,
			SuggestedGroup: string(col.SuggestedGroup),
		})
	}
	for _, pin := range opts.Pins {
		out.PinOptions = append(out.PinOptions, StatusPinOption{Name: pin.Name, Category: pin.Category})
	}
	return out
}
