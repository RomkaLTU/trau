package webserver

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// grillProposal is one panel member's turn in a second-opinion session: the provider
// that decided it, the round it belongs to, and the finish_session-shaped payload
// itself. Round 0 is the draft phase; a challenge round's turn either carries a
// revised outcome with the challenge_note saying what it disputes, or endorses
// another member's proposal and carries no outcome of its own. The session's
// canonical decision stays a single kind=outcome row, so a proposal never settles
// anything on its own and the apply path reads exactly what it always did.
type grillProposal struct {
	Provider      string          `json:"provider"`
	Round         int             `json:"round"`
	Outcome       json.RawMessage `json:"outcome,omitempty"`
	Endorse       string          `json:"endorse,omitempty"`
	ChallengeNote string          `json:"challenge_note,omitempty"`
}

// GrillProposalView is one panel turn as the review reads it: which provider decided
// it and what it decided, with the message id the choose call names it by.
type GrillProposalView struct {
	MessageID     string          `json:"message_id"`
	Provider      string          `json:"provider"`
	Round         int             `json:"round"`
	Outcome       json.RawMessage `json:"outcome,omitempty"`
	Endorse       string          `json:"endorse,omitempty"`
	ChallengeNote string          `json:"challenge_note,omitempty"`
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

const grillDecisionDispositions = "disposition is one of: " +
	"\"rewrite\" (replace the issue description — requires proposed_description), \"split\" (the issue is " +
	"epic-shaped; convert it to an epic and propose fully-specified sub-issues — requires " +
	"proposed_description framing the epic and a non-empty sub_issues breakdown), \"needs_split\" (too large " +
	"to slice confidently; just flag it for splitting), \"create\" (author a brand-new issue — requires " +
	"title and proposed_description; add a sub_issues breakdown to file it as an epic instead of a single " +
	"issue), \"research\" (what the session produced is a report, not an issue body — requires title and " +
	"findings), or \"no_change\" (nothing needs writing). summary captures the key clarifications the " +
	"interview reached. Nothing is written to the tracker until the user approves."

// grillMemberMCPTools is one panel member's tool surface for one phase. In the draft
// phase the member decides alone, so submit_decision is the plain decision contract.
// In a challenge round the member has read the competing proposals, so the same call
// takes one of two shapes: endorse names the proposal it adopts as-is, or a revised
// decision carries the challenge_note saying what it disputes.
func grillMemberMCPTools(panel []string, round int) []mcpTool {
	description := "Submit your independent decision on this issue's outcome, for the user to review beside the " +
		"other participants' proposals. Call it exactly once, then end your turn. " + grillDecisionDispositions
	if round > 0 {
		description = "Submit your verdict on this challenge round, exactly once, then end your turn. Either " +
			"endorse one of the proposals you were shown — pass endorse with that provider's name and nothing " +
			"else — or submit a revised decision of your own together with challenge_note saying what you " +
			"dispute in the proposals you rejected. A revision keeps you behind your own proposal. " +
			"For a revision, " + grillDecisionDispositions
	}
	return []mcpTool{{
		Name:        "submit_decision",
		Description: description,
		InputSchema: grillMemberDecisionSchema(panel, round),
	}}
}

// grillMemberDecisionSchema is the shared decision shape, plus the two fields a
// challenge round decides with. A round turn may carry no disposition at all — an
// endorsement adopts another member's — so the round schema drops the required set
// and the hub validates the two shapes itself, returning a tool error the member can
// correct.
func grillMemberDecisionSchema(panel []string, round int) json.RawMessage {
	if round < 1 {
		return grillDecisionSchema
	}
	var schema map[string]any
	if json.Unmarshal(grillDecisionSchema, &schema) != nil {
		return grillDecisionSchema
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return grillDecisionSchema
	}
	props["endorse"] = map[string]any{
		"type":        "string",
		"enum":        panel,
		"description": "The panel member whose current proposal you adopt as-is. Set it alone; omit every other field.",
	}
	props["challenge_note"] = map[string]any{
		"type": "string",
		"description": "Required when you submit a revision instead of endorsing: what you dispute in the " +
			"proposals you rejected, in a sentence or two.",
	}
	delete(schema, "required")
	out, _ := json.Marshal(schema)
	return out
}

// handleGrillMemberMCP serves one panel member's MCP endpoint (POST
// /grill/{sid}/mcp/{member} for the draft phase, /grill/{sid}/mcp/{member}/{round}
// for challenge round {round}). It exposes submit_decision alone: a member decides
// against the material it was handed and never talks to the user, so it has no
// question tools and no way to finish the session itself. The path scopes the call to
// one session, one member and one round, so a turn can only record its own verdict on
// the round it was spawned for.
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
	round, ok := parseGrillRound(w, r.PathValue("round"))
	if !ok {
		return
	}
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return
	}
	// The draft phase is the challengers' alone; a challenge round is the whole panel's,
	// interviewer included, since every member answers the same contest.
	panel := grillPanel(sess)
	known := slices.Contains(sess.Challengers, member) || (round > 0 && slices.Contains(panel, member))
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown second opinion for this session"})
		return
	}
	mcpServer{
		name:    "trau-grill",
		version: s.version,
		tools:   grillMemberMCPTools(panel, round),
		callTool: func(w http.ResponseWriter, _ *http.Request, rpcID json.RawMessage, p toolsCallParams) {
			if p.Name != "submit_decision" {
				respondRPCError(w, rpcID, rpcInvalidParams, "unknown tool: "+p.Name)
				return
			}
			s.grillSubmitDecision(w, sid, member, round, rpcID, p.Arguments)
		},
	}.serve(w, r)
}

// grillSubmitDecision records one panel member's turn. An endorsement adopts another
// member's current proposal and stores no outcome of its own; anything else is
// validated exactly as finish_session validates it and stored as that member's
// proposal for the round. The phase around this call, not the call, is what finishes
// the session.
func (s *Server) grillSubmitDecision(w http.ResponseWriter, sid int64, member string, round int, rpcID, args json.RawMessage) {
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
	cycle := grillActiveCycle(msgs)
	// A client that retried a call the hub already answered must not speak twice: every
	// member gets one turn per round, and unanimity counts one vote each.
	if grillSubmittedIn(cycle, member, round) {
		respondRPCJSON(w, rpcID, mcpToolError("you have already submitted your decision for this round"))
		return
	}
	var a struct {
		Endorse       string `json:"endorse"`
		ChallengeNote string `json:"challenge_note"`
	}
	if json.Unmarshal(args, &a) != nil {
		respondRPCJSON(w, rpcID, mcpToolError("submit_decision arguments were not valid JSON"))
		return
	}
	note := strings.TrimSpace(a.ChallengeNote)
	if endorse := strings.TrimSpace(a.Endorse); endorse != "" {
		s.grillEndorse(w, sid, member, endorse, round, cycle, rpcID)
		return
	}
	outcome, disposition, errMsg := grillDecisionOutcome("submit_decision", args)
	if errMsg != "" {
		respondRPCJSON(w, rpcID, mcpToolError(errMsg))
		return
	}
	if round > 0 && note == "" {
		respondRPCJSON(w, rpcID, mcpToolError(
			"a revision in a challenge round requires challenge_note: what you dispute in the proposals you did "+
				"not endorse. Endorse one of them instead if you dispute nothing."))
		return
	}
	msg, err := s.appendGrillPanelTurn(sid, grillProposal{
		Provider:      member,
		Round:         round,
		Outcome:       json.RawMessage(outcome),
		ChallengeNote: note,
	})
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "store proposal: "+err.Error())
		return
	}
	s.publishGrillMessage(msg)
	respondRPCJSON(w, rpcID, mcpToolSuccess(
		"Recorded your decision (\""+disposition+"\"). End your turn now: do not call any more tools."))
}

// grillEndorse records one member adopting another's proposal as-is. The endorsed
// provider has to be holding a proposal right now — a name nobody drafted, or one a
// later revision replaced, would resolve to nothing when the round is counted.
func (s *Server) grillEndorse(
	w http.ResponseWriter,
	sid int64,
	member, endorse string,
	round int,
	cycle []hubstore.GrillMessage,
	rpcID json.RawMessage,
) {
	if round < 1 {
		respondRPCJSON(w, rpcID, mcpToolError("there is nothing to endorse yet: submit your own decision"))
		return
	}
	holders := []string{}
	for _, p := range grillCurrentProposals(cycle) {
		holders = append(holders, p.Provider)
	}
	if !slices.Contains(holders, endorse) {
		respondRPCJSON(w, rpcID, mcpToolError(
			"endorse must name a panel member holding a proposal: "+strings.Join(holders, ", ")))
		return
	}
	msg, err := s.appendGrillPanelTurn(sid, grillProposal{Provider: member, Round: round, Endorse: endorse})
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "store endorsement: "+err.Error())
		return
	}
	s.publishGrillMessage(msg)
	respondRPCJSON(w, rpcID, mcpToolSuccess(
		"Recorded your endorsement of "+endorse+"'s proposal. End your turn now: do not call any more tools."))
}

// grillInterviewerProposal records the interviewer's own decision in a session that
// has challengers. The session stays running: the interview is over, but the panel
// phases that follow are what finish it, so nothing is presented for review until the
// second opinions are in and the challenge rounds have run.
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
			"and the panel then weighs them against each other. End your turn now: do not call any "+
			"more tools."))
}

// appendGrillProposal records a draft-phase proposal, the round-0 turn every panel
// member opens with.
func (s *Server) appendGrillProposal(sid int64, provider, outcome string) (hubstore.GrillMessage, error) {
	return s.appendGrillPanelTurn(sid, grillProposal{
		Provider: provider,
		Round:    0,
		Outcome:  json.RawMessage(outcome),
	})
}

func (s *Server) appendGrillPanelTurn(sid int64, turn grillProposal) (hubstore.GrillMessage, error) {
	payload, err := json.Marshal(turn)
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

// grillProposalViews narrows a conversation's panel turns to the ones carrying a
// proposal of their own, in the order they landed; an endorsement adopts another
// member's and has none.
func grillProposalViews(msgs []hubstore.GrillMessage) []GrillProposalView {
	out := []GrillProposalView{}
	for _, t := range grillPanelTurns(msgs) {
		if len(t.Outcome) > 0 {
			out = append(out, t)
		}
	}
	return out
}

func grillSubmittedIn(msgs []hubstore.GrillMessage, provider string, round int) bool {
	return slices.ContainsFunc(grillPanelTurns(msgs), func(t GrillProposalView) bool {
		return t.Provider == provider && t.Round == round
	})
}

// parseGrillRound reads the optional {round} path segment. An absent segment is the
// draft phase (round 0); anything else has to name a challenge round.
func parseGrillRound(w http.ResponseWriter, raw string) (int, bool) {
	if raw == "" {
		return 0, true
	}
	round, err := strconv.Atoi(raw)
	if err != nil || round < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid challenge round"})
		return 0, false
	}
	return round, true
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
	cycle := grillActiveCycle(msgs)
	return len(grillProposalViews(cycle)) > 0 && !grillDecided(cycle)
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
	cycle := grillPanelCycle(sess, msgs)
	proposals := grillCurrentProposals(cycle)
	if len(proposals) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no proposals to choose from"})
		return
	}
	if grillDecided(cycle) {
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
