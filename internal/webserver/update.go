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

// UpdateStatus is the /api/v1/update resource: what the checker knows about
// newer versions, plus the repo a self-reload is waiting on and the build
// channel this hub runs. Both live on the hub rather than the checker — a merge
// asks for the reload, and the channel is a fact about where the executable
// sits, not about any release.
type UpdateStatus struct {
	update.Status
	SelfReloadPending string        `json:"selfReloadPending"`
	Channel           string        `json:"channel"`
	ChannelRepo       string        `json:"channelRepo"`
	ChannelRepos      []ChannelRepo `json:"channelRepos"`
	ChannelSwitch     ChannelSwitch `json:"channelSwitch"`
}

func (s *Server) updateStatus() UpdateStatus {
	channel, channelRepo := s.hubChannel()
	return UpdateStatus{
		Status:            s.updates.Status(),
		SelfReloadPending: s.selfReloadPending(),
		Channel:           channel,
		ChannelRepo:       channelRepo,
		ChannelRepos:      s.eligibleChannelRepos(),
		ChannelSwitch:     s.channelSwitch(),
	}
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, s.updateStatus())
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
	writeJSON(w, http.StatusOK, s.updateStatus())
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
			"error":        "trau cannot update this install itself",
			"instructions": manualUpdateInstructions(s.updates.Status()),
		})
	case errors.Is(err, update.ErrApplyInFlight):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "an update is already being applied"})
	default:
		writeJSON(w, http.StatusAccepted, s.updateStatus())
	}
}

// manualUpdateInstructions is what to do about an install trau will not update
// itself: the owning manager's own upgrade command when one is known, and the
// release page when the install belongs to no manager trau recognises.
func manualUpdateInstructions(st update.Status) string {
	if st.UpgradeCommand != "" {
		return "run `" + st.UpgradeCommand + "`, then restart the hub"
	}
	releaseURL := st.ReleaseURL
	if releaseURL == "" {
		releaseURL = update.ReleasesURL
	}
	return "update trau the way you installed it, then restart the hub: " + releaseURL
}
