package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// defaultPregrillMax bounds a pre-grill pass when the repo config leaves
// GRILL_PREGRILL_MAX unset or non-positive.
const defaultPregrillMax = 5

// Per-issue outcomes a pre-grill pass reports. A question parked and a rewrite
// drafted both leave a session in the inbox; clear needed no rewrite; skipped means
// the issue already had an active session or fell past the pass limit.
const (
	pregrillOutcomeQuestion = "question_parked"
	pregrillOutcomeRewrite  = "rewrite_drafted"
	pregrillOutcomeClear    = "clear"
	pregrillOutcomeError    = "error"
	pregrillOutcomeSkipped  = "skipped"
)

// PregrillRequest is the body of POST /repos/{repo}/grill/pregrill: the issues to
// pre-grill, in order. The per-item button sends one; "pre-grill all" sends the
// inbox's untouched issues. The pass bounds the list to GRILL_PREGRILL_MAX turns.
type PregrillRequest struct {
	IssueIDs []string `json:"issue_ids"`
}

// PregrillResult is one issue's outcome from the pass.
type PregrillResult struct {
	IssueID   string `json:"issue_id"`
	SessionID string `json:"session_id,omitempty"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail,omitempty"`
}

// PregrillResponse reports the pass: the turn budget it honoured and each issue's
// outcome.
type PregrillResponse struct {
	Repo    string           `json:"repo"`
	Max     int              `json:"max"`
	Results []PregrillResult `json:"results"`
}

// handleRepoPregrill runs an AFK pre-grill pass over the requested issues (POST). It
// is a bounded, sequential sweep: each issue gets a normal grilling turn whose
// opening question parks at once (no user present) or that finishes with a rewrite
// or no_change proposal. The session states it leaves are what the inbox surfaces.
func (s *Server) handleRepoPregrill(w http.ResponseWriter, r *http.Request) {
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
	var req PregrillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	max := s.pregrillMax(repo)
	results := s.runPregrillPass(r.Context(), repo, req.IssueIDs, max)
	writeJSON(w, http.StatusOK, PregrillResponse{Repo: repo.Name, Max: max, Results: results})
}

// pregrillMax resolves the repo's pass bound, defaulting when config leaves it unset
// or non-positive so a misconfigured 0 never silently disables pre-grilling.
func (s *Server) pregrillMax(repo registry.Repo) int {
	cfg, err := s.grillConfigFor(repo)
	if err != nil || cfg.GrillPregrillMax <= 0 {
		return defaultPregrillMax
	}
	return cfg.GrillPregrillMax
}

// runPregrillPass grills the issues in order until the turn budget is spent. An
// issue that already has an active session is skipped without spending budget; once
// the budget is gone the rest are reported skipped. Each grilled issue runs its turn
// synchronously so the settled session can be classified into an outcome.
func (s *Server) runPregrillPass(ctx context.Context, repo registry.Repo, issueIDs []string, max int) []PregrillResult {
	results := make([]PregrillResult, 0, len(issueIDs))
	budget := max
	for _, raw := range issueIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if budget <= 0 {
			results = append(results, PregrillResult{IssueID: id, Outcome: pregrillOutcomeSkipped, Detail: "pre-grill pass limit reached"})
			continue
		}
		sess, err := s.stores.Grill().Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: id})
		if errors.Is(err, hubstore.ErrGrillActiveSession) {
			results = append(results, PregrillResult{IssueID: id, Outcome: pregrillOutcomeSkipped, Detail: "already has an active grill session"})
			continue
		}
		if err != nil {
			results = append(results, PregrillResult{IssueID: id, Outcome: pregrillOutcomeError, Detail: err.Error()})
			continue
		}
		budget--
		s.markPregrill(sess.ID)
		if s.runGrillTurn != nil {
			s.runGrillTurn(ctx, sess)
		}
		s.clearPregrill(sess.ID)
		results = append(results, s.classifyPregrill(id, sess.ID))
	}
	return results
}

// classifyPregrill reads the session's settled state and its proposed disposition
// into a per-issue outcome.
func (s *Server) classifyPregrill(issueID string, sid int64) PregrillResult {
	res := PregrillResult{IssueID: issueID, SessionID: strconv.FormatInt(sid, 10)}
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil || !found {
		res.Outcome = pregrillOutcomeError
		res.Detail = "the pre-grill session could not be read back"
		return res
	}
	disposition := ""
	if sess.State == hubstore.GrillFinished {
		disposition = s.lastGrillDisposition(sid)
	}
	res.Outcome, res.Detail = classifyPregrillOutcome(sess, disposition)
	return res
}

// classifyPregrillOutcome maps a settled session and its disposition onto the pass's
// outcome vocabulary. A finished session is a proposal (no_change reads as clear,
// anything else as a drafted rewrite); an idle-parked session left a question
// waiting; a crash, no-outcome or stall park carries a reason and reads as an error.
func classifyPregrillOutcome(sess hubstore.GrillSession, disposition string) (outcome, detail string) {
	switch sess.State {
	case hubstore.GrillFinished, hubstore.GrillApplied:
		if disposition == grillDispNoChange {
			return pregrillOutcomeClear, ""
		}
		return pregrillOutcomeRewrite, disposition
	case hubstore.GrillWaiting, hubstore.GrillParked:
		if strings.TrimSpace(sess.ParkedReason) == "" {
			return pregrillOutcomeQuestion, ""
		}
		return pregrillOutcomeError, sess.ParkedReason
	case hubstore.GrillStalled:
		return pregrillOutcomeError, sess.ParkedReason
	default:
		return pregrillOutcomeError, "the pre-grill turn ended without a question or proposal"
	}
}

// lastGrillDisposition returns the disposition of a session's most recent outcome
// message, or empty when it has none.
func (s *Server) lastGrillDisposition(sid int64) string {
	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind != hubstore.GrillKindOutcome {
			continue
		}
		var p struct {
			Disposition string `json:"disposition"`
		}
		_ = json.Unmarshal([]byte(msgs[i].Payload), &p)
		return p.Disposition
	}
	return ""
}

// markPregrill / clearPregrill / isPregrill flag the sessions whose first ask_user
// must park immediately (the AFK pass). The flag is process-local and lives only for
// the turn: firstPrompt reads it to pick the pre-grill prompt and grillAskUser reads
// it to park at once instead of blocking for the full idle window.
func (s *Server) markPregrill(sid int64) {
	s.pregrillMu.Lock()
	s.pregrill[sid] = true
	s.pregrillMu.Unlock()
}

func (s *Server) clearPregrill(sid int64) {
	s.pregrillMu.Lock()
	delete(s.pregrill, sid)
	s.pregrillMu.Unlock()
}

func (s *Server) isPregrill(sid int64) bool {
	s.pregrillMu.Lock()
	defer s.pregrillMu.Unlock()
	return s.pregrill[sid]
}
