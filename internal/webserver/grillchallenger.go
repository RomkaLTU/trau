package webserver

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// grillProposal is one participant's draft outcome in a second-opinion session: the
// provider that decided it, the round it belongs to, and the
// finish_session-shaped payload itself. The session's canonical decision stays a
// single kind=outcome row, so a proposal never settles anything on its own and the
// apply path reads exactly what it always did.
type grillProposal struct {
	Provider string          `json:"provider"`
	Round    int             `json:"round"`
	Outcome  json.RawMessage `json:"outcome"`
}

// GrillProposalView is one proposal as the review reads it: which provider drafted it
// and the outcome it drafted, with the message id the choose call names it by.
type GrillProposalView struct {
	MessageID string          `json:"message_id"`
	Provider  string          `json:"provider"`
	Round     int             `json:"round"`
	Outcome   json.RawMessage `json:"outcome"`
}

// GrillChooseProposalRequest is the body of POST /grill/{sid}/choose-proposal: the id
// of the proposal message the user picked.
type GrillChooseProposalRequest struct {
	MessageID string `json:"message_id"`
}

// GrillChooseProposalResponse reports the session and the canonical outcome message
// the choice minted, which is what the ordinary editable review then rides.
type GrillChooseProposalResponse struct {
	Session GrillSessionView `json:"session"`
	Message GrillMessageView `json:"message"`
}

var grillMemberMCPTools = []mcpTool{
	{
		Name: "submit_decision",
		Description: "Submit your independent decision on this issue's outcome, for the user to review beside the " +
			"other participants' proposals. Call it exactly once, then end your turn. disposition is one of: " +
			"\"rewrite\" (replace the issue description — requires proposed_description), \"split\" (the issue is " +
			"epic-shaped; convert it to an epic and propose fully-specified sub-issues — requires " +
			"proposed_description framing the epic and a non-empty sub_issues breakdown), \"needs_split\" (too large " +
			"to slice confidently; just flag it for splitting), \"create\" (author a brand-new issue — requires " +
			"title and proposed_description; add a sub_issues breakdown to file it as an epic instead of a single " +
			"issue), \"research\" (what the session produced is a report, not an issue body — requires title and " +
			"findings), or \"no_change\" (nothing needs writing). summary captures the key clarifications the " +
			"interview reached. Nothing is written to the tracker until the user approves.",
		InputSchema: grillDecisionSchema,
	},
}

// handleGrillMemberMCP serves one challenger's MCP endpoint (POST
// /grill/{sid}/mcp/{member}). It exposes submit_decision alone: a challenger drafts
// against the interview transcript it was handed and never talks to the user, so it
// has no question tools and no way to finish the session itself. The path scopes the
// call to one session and one member, so a draft can only record its own proposal.
func (s *Server) handleGrillMemberMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
	if !ok {
		return
	}
	member := strings.TrimSpace(r.PathValue("member"))
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return
	}
	if !slices.Contains(sess.Challengers, member) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown second opinion for this session"})
		return
	}
	mcpServer{
		name:    "trau-grill",
		version: s.version,
		tools:   grillMemberMCPTools,
		callTool: func(w http.ResponseWriter, _ *http.Request, rpcID json.RawMessage, p toolsCallParams) {
			if p.Name != "submit_decision" {
				respondRPCError(w, rpcID, rpcInvalidParams, "unknown tool: "+p.Name)
				return
			}
			s.grillSubmitDecision(w, sid, member, rpcID, p.Arguments)
		},
	}.serve(w, r)
}

// grillSubmitDecision records one challenger's draft outcome. It validates exactly
// what finish_session validates and stores the result as a proposal; the draft phase,
// not this call, is what finishes the session.
func (s *Server) grillSubmitDecision(w http.ResponseWriter, sid int64, member string, rpcID, args json.RawMessage) {
	outcome, disposition, errMsg := grillDecisionOutcome("submit_decision", args)
	if errMsg != "" {
		respondRPCJSON(w, rpcID, mcpToolError(errMsg))
		return
	}
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, err.Error())
		return
	}
	if !found {
		respondRPCError(w, rpcID, rpcInternalError, "grill session not found")
		return
	}
	if sess.State != hubstore.GrillRunning {
		respondRPCJSON(w, rpcID, mcpToolError("this session is no longer collecting second opinions"))
		return
	}
	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, err.Error())
		return
	}
	// A client that retried a call the hub already answered must not draft twice: the
	// review lists one proposal per participant.
	if grillProposedBy(msgs, member) {
		respondRPCJSON(w, rpcID, mcpToolError("you have already submitted your decision for this session"))
		return
	}
	msg, err := s.appendGrillProposal(sid, member, outcome)
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "store proposal: "+err.Error())
		return
	}
	s.publishGrillMessage(msg)
	respondRPCJSON(w, rpcID, mcpToolSuccess(
		"Recorded your decision (\""+disposition+"\"). End your turn now: do not call any more tools."))
}

// grillInterviewerProposal records the interviewer's own decision in a session that
// has challengers. The session stays running: the interview is over, but the draft
// phase that follows is what finishes it, so nothing is presented for review until
// every second opinion is in.
func (s *Server) grillInterviewerProposal(
	w http.ResponseWriter,
	sess hubstore.GrillSession,
	rpcID json.RawMessage,
	outcome, disposition string,
) {
	msg, err := s.appendGrillProposal(sess.ID, grillEffectiveProvider(sess.Provider), outcome)
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "store proposal: "+err.Error())
		return
	}
	s.publishGrillMessage(msg)
	respondRPCJSON(w, rpcID, mcpToolSuccess(
		"Recorded your decision (\""+disposition+"\") as this session's first proposal. "+
			strings.Join(sess.Challengers, " and ")+" now draft their own from the same interview, "+
			"and the user reviews them side by side. End your turn now: do not call any more tools."))
}

func (s *Server) appendGrillProposal(sid int64, provider, outcome string) (hubstore.GrillMessage, error) {
	payload, err := json.Marshal(grillProposal{
		Provider: provider,
		Round:    0,
		Outcome:  json.RawMessage(outcome),
	})
	if err != nil {
		return hubstore.GrillMessage{}, err
	}
	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindProposal,
		Payload: string(payload),
	})
	return msg, err
}

// grillProposalViews reads a conversation's proposals in the order they landed. A row
// whose payload will not parse is dropped rather than failing the read: the review
// still has the proposals that did.
func grillProposalViews(msgs []hubstore.GrillMessage) []GrillProposalView {
	out := []GrillProposalView{}
	for _, m := range msgs {
		if m.Kind != hubstore.GrillKindProposal {
			continue
		}
		var p grillProposal
		if json.Unmarshal([]byte(m.Payload), &p) != nil || len(p.Outcome) == 0 {
			continue
		}
		out = append(out, GrillProposalView{
			MessageID: strconv.FormatInt(m.ID, 10),
			Provider:  p.Provider,
			Round:     p.Round,
			Outcome:   p.Outcome,
		})
	}
	return out
}

func grillProposedBy(msgs []hubstore.GrillMessage, provider string) bool {
	for _, p := range grillProposalViews(msgs) {
		if p.Provider == provider {
			return true
		}
	}
	return false
}

// grillDecided reports whether a canonical outcome supersedes the session's proposals
// — the row the apply path reads and the choose call mints.
func grillDecided(msgs []hubstore.GrillMessage) bool {
	for _, m := range msgs {
		if m.Kind == hubstore.GrillKindOutcome {
			return true
		}
	}
	return false
}

// grillDraftsPending reports whether the session is still owed its second opinions:
// the interviewer has proposed into a challenger session that nothing has decided or
// settled yet. It keeps the turn reconcile from reading a proposal-and-exit as a turn
// that ended without proposing anything, and it is what opens the draft phase.
func (s *Server) grillDraftsPending(sess hubstore.GrillSession) bool {
	if len(sess.Challengers) == 0 || sess.State != hubstore.GrillRunning {
		return false
	}
	msgs, err := s.stores.Grill().Messages(sess.ID, 0)
	if err != nil {
		return false
	}
	return len(grillProposalViews(msgs)) > 0 && !grillDecided(msgs)
}

// handleGrillChooseProposal promotes one of a finished session's proposals to the
// session's canonical outcome (POST /grill/{sid}/choose-proposal). It copies the
// chosen payload into a fresh kind=outcome row and changes nothing else, so the
// ordinary editable review and Apply proceed on it exactly as on a solo session's.
func (s *Server) handleGrillChooseProposal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return
	}
	if sess.State != hubstore.GrillFinished {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is not awaiting a proposal choice"})
		return
	}
	var req GrillChooseProposalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	msgs, err := s.stores.Grill().Messages(sess.ID, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	proposals := grillProposalViews(msgs)
	if len(proposals) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no proposals to choose from"})
		return
	}
	if grillDecided(msgs) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a proposal has already been chosen"})
		return
	}
	wanted := strings.TrimSpace(req.MessageID)
	idx := slices.IndexFunc(proposals, func(p GrillProposalView) bool { return p.MessageID == wanted })
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown proposal"})
		return
	}
	msg, _, err := s.stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindOutcome,
		Payload: string(proposals[idx].Outcome),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store outcome: " + err.Error()})
		return
	}
	s.publishGrillMessage(msg)
	writeJSON(w, http.StatusOK, GrillChooseProposalResponse{
		Session: s.grillSessionView("", sess),
		Message: grillMessageView(msg),
	})
}
