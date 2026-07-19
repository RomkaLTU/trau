package webserver

import (
	"errors"
	"net/http"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/update"
)

// SetUpdateChecks gates the daily GitHub release check (UPDATE_CHECK). Call it
// before Start. On-disk drift detection is never gated: it involves no network.
func (s *Server) SetUpdateChecks(enabled bool) {
	s.updates.SetEnabled(enabled)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, s.updates.Status())
}

// handleUpdateCheck forces a release check and answers with the resulting state.
// A failed fetch still answers 200 with the last known result: an unreachable
// GitHub is not something the UI should surface as an error.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if err := s.updates.CheckNow(r.Context()); err != nil {
		logger.Verbosef("update check: %v", err)
	}
	writeJSON(w, http.StatusOK, s.updates.Status())
}

// handleUpdateApply hands the upgrade to Homebrew and restarts onto the result.
// It answers before the upgrade finishes; applyState on /update carries the rest.
// The restart it ends with is unconditional — clients warn about active runs and
// confirm before they ever POST here.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	switch err := s.updates.Apply(s.triggerRestart); {
	case errors.Is(err, update.ErrNotBrew):
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":        "not a Homebrew install",
			"instructions": manualUpdateInstructions(s.updates.Status().ReleaseURL),
		})
	case errors.Is(err, update.ErrApplyInFlight):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is already being applied"})
	default:
		writeJSON(w, http.StatusAccepted, s.updates.Status())
	}
}

func manualUpdateInstructions(releaseURL string) string {
	if releaseURL == "" {
		releaseURL = update.ReleasesURL
	}
	return "update trau the way you installed it, then restart the hub: " + releaseURL
}
