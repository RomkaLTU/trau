package webserver

import (
	"net/http"

	"github.com/RomkaLTU/trau/internal/logger"
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
