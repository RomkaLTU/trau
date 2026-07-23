package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/RomkaLTU/trau/internal/state"
)

// AdvanceRequest is the body of a hand-back choice. Rerun clears the takeover
// stamp but leaves the phase put, so the run re-enters the interrupted phase.
type AdvanceRequest struct {
	Rerun bool `json:"rerun"`
}

// AdvanceResult is the outcome of a hand-back choice: the phase the checkpoint
// held and the one it holds now, identical on the re-run branch.
type AdvanceResult struct {
	Ticket string `json:"ticket"`
	From   string `json:"from"`
	Phase  string `json:"phase"`
}

// handleAdvanceRun settles the hand-back of a ticket a human steered in a
// takeover terminal (ADR 0018): it records the interrupted phase as finished so
// the run picks up at the next step, or leaves the phase where it is on the
// re-run branch, and either way clears the TAKEOVER stamp so the next hand-back
// does not re-prompt. The `takeover` ANOMALIES marker stays for run history.
//
// Advancing is the operator asserting that phase's exit criteria — nothing here
// inspects the tree — and the downstream phases keep their own validation.
func (s *Server) handleAdvanceRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ticket, ok := s.mutableCheckpoint(w, r)
	if !ok {
		return
	}
	var req AdvanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	row, _, err := s.stores.Checkpoints().One(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	data := map[string]string{}
	if row.Data != "" {
		_ = json.Unmarshal([]byte(row.Data), &data)
	}
	if data["TAKEOVER"] == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  fmt.Sprintf("%s has not been steered in a terminal since its last phase transition", ticket),
			"reason": "no_takeover",
		})
		return
	}

	phase := row.Phase
	if !req.Rerun {
		phase = state.AdvancedPhase(row.Phase)
		if phase == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":  fmt.Sprintf("%s stopped at %q, which has no completed phase to advance to", ticket, row.Phase),
				"reason": "unmappable_phase",
			})
			return
		}
	}
	data["PHASE"] = phase
	delete(data, "TAKEOVER")
	data["UPDATED"] = time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := s.stores.Checkpoints().Upsert(repo.Root, ticket, data); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "advance failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, AdvanceResult{Ticket: ticket, From: row.Phase, Phase: phase})
}
