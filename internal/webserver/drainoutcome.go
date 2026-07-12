package webserver

import (
	"encoding/json"
	"net/http"
)

// drainOutcomeBody carries a queued child's exit outcome on the wire — it mirrors
// hubclient.drainOutcomeBody, both request and response.
type drainOutcomeBody struct {
	Class  string `json:"class,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// handleRunDrainOutcome is the queued child's seam for reporting how it exited
// (ADR 0008). The child PUTs its outcome — a fault or provider pause it hit, or a
// clean finish (an empty class) — as it exits, keyed by ticket; the drainer reads
// and clears it in-process to settle the item. Like an artifact write it comes
// from the live loop, so it is not refused while a loop is live.
func (s *Server) handleRunDrainOutcome(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	ticket := r.PathValue("ticket")
	outcomes := s.stores.DrainOutcomes()

	switch r.Method {
	case http.MethodGet:
		rep, found, err := outcomes.One(repo.Root, ticket)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no drain outcome"})
			return
		}
		writeJSON(w, http.StatusOK, drainOutcomeBody{Class: rep.Class, Reason: rep.Reason})
	case http.MethodPut:
		var req drainOutcomeBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if err := outcomes.Upsert(repo.Root, ticket, req.Class, req.Reason); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ticket": ticket})
	case http.MethodDelete:
		if err := outcomes.Remove(repo.Root, ticket); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "ticket": ticket})
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}
