package webserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// Grilling outcome dispositions a child can finish a session with. create files a
// brand-new issue (or epic) from an authoring session with no anchor; research
// delivers a report rather than a change to an issue body.
const (
	grillDispRewrite    = "rewrite"
	grillDispSplit      = "split"
	grillDispNeedsSplit = "needs_split"
	grillDispCreate     = "create"
	grillDispResearch   = "research"
	grillDispNoChange   = "no_change"
)

// grillAskIdleTimeout bounds how long a blocked ask_user waits for an answer
// before parking the session and returning the park sentinel. A blocked call
// costs zero tokens, so the window is generous; the user answering later fires a
// resume turn. grillAskKeepalive is how often the blocked call emits a keepalive
// so the client's HTTP idle timeout never fires early. Both are vars so tests can
// shorten them.
var (
	grillAskIdleTimeout = 10 * time.Minute
	grillAskKeepalive   = time.Minute
)

var grillMCPTools = []mcpTool{
	{
		Name: "ask_user",
		Description: "Ask the user exactly one clarifying question and wait for their answer, which is returned as the tool result. " +
			"One question per call and per question string: never bundle several questions into one question value — no \"Also, ...?\" " +
			"tacked on the end. If the user has stepped away the call returns a park instruction: end your turn then " +
			"without asking again — the question is saved and the session resumes with their answer when they return.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "The single question to ask the user."},
    "options": {"type": "array", "items": {"type": "string"}, "description": "Optional suggested answers to offer the user."},
    "recommended": {"type": "string", "description": "Optional: when you offer options, the one you would choose — repeat that option's text exactly. Omit only for pure-preference questions."},
    "why": {"type": "string", "description": "Optional one-line reason for the recommended option."},
    "allow_free_text": {"type": "boolean", "description": "Whether the user may answer freely instead of picking an option. Defaults to true."}
  },
  "required": ["question"]
}`),
	},
	{
		Name: "finish_session",
		Description: "End the grilling session with a proposed outcome for the user to review. disposition is one of: " +
			"\"rewrite\" (replace the issue description — requires proposed_description), \"split\" (the issue is epic-shaped; " +
			"convert it to an epic and propose fully-specified sub-issues — requires proposed_description framing the epic and " +
			"a non-empty sub_issues breakdown), \"needs_split\" (too large to slice confidently; just flag it for splitting), " +
			"\"create\" (author a brand-new issue from a from-scratch session — requires title and proposed_description; add a " +
			"sub_issues breakdown to file it as an epic instead of a single issue), \"research\" (the session's work was " +
			"investigation and what it produced is a report, not an issue body — requires title and findings), or \"no_change\" " +
			"(nothing needs writing). summary captures the key clarifications reached. Nothing is written to the tracker " +
			"until the user approves.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "disposition": {"type": "string", "enum": ["rewrite", "split", "needs_split", "create", "research", "no_change"], "description": "The proposed outcome."},
    "title": {"type": "string", "description": "Required when disposition is create (the title of the new issue or epic to file) or research (the report's own title — what the reader sees at the top of the document and in the report list, not the question verbatim)."},
    "proposed_description": {"type": "string", "description": "Required when disposition is rewrite (the full replacement issue description), split (the parent rewrite framing the epic goal), or create (the full description of the new issue or epic)."},
    "findings": {"type": "string", "description": "Required when disposition is research: the complete Markdown research report — the question, what was investigated, the sources consulted, the conclusions, and the recommendation."},
    "labels": {"type": "array", "items": {"type": "string"}, "description": "Optional labels for the created issue when disposition is create. A single issue defaults to the ready-for-agent label; an epic parent gets none by default."},
    "sub_issues": {
      "type": "array",
      "description": "Required for split, optional for create: the proposed breakdown, one implementable slice per agent session. Every slice must be a thin VERTICAL slice: end-to-end and independently verifiable on its own. A horizontal layer is not a slice — \"schema\", \"backend\", \"UI\" are layers. Each becomes a child of the parent (the grilled issue for split, the newly created epic for create).",
      "items": {
        "type": "object",
        "properties": {
          "title": {"type": "string", "description": "The sub-issue title."},
          "description": {"type": "string", "description": "The full, unambiguous slice description an agent can implement without guessing."},
          "labels": {"type": "array", "items": {"type": "string"}, "description": "Labels for the sub-issue. Defaults to the ready-for-agent label when omitted."},
          "blocked_by": {"type": "array", "items": {"type": "integer"}, "description": "Zero-based indices of sibling sub-issues in this array that must finish before this one can start."}
        },
        "required": ["title", "description"]
      }
    },
    "summary": {"type": "string", "description": "A short summary of the clarifications reached during the session."}
  },
  "required": ["disposition", "summary"]
}`),
	},
}

// handleGrillMCP serves the per-session MCP endpoint (POST /grill/{sid}/mcp). The
// session id in the path scopes every tool call to one session, so a child can
// only touch its own. It rides the same bearer-token gate as the rest of the API.
func (s *Server) handleGrillMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
	if !ok {
		return
	}
	if _, found, err := s.stores.Grill().Session(sid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	} else if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return
	}
	mcpServer{
		name:    "trau-grill",
		version: s.version,
		tools:   grillMCPTools,
		callTool: func(w http.ResponseWriter, r *http.Request, rpcID json.RawMessage, p toolsCallParams) {
			s.grillMCPToolsCall(w, r, sid, rpcID, p)
		},
	}.serve(w, r)
}

func (s *Server) grillMCPToolsCall(w http.ResponseWriter, r *http.Request, sid int64, rpcID json.RawMessage, p toolsCallParams) {
	switch p.Name {
	case "ask_user":
		s.grillAskUser(w, r, sid, rpcID, p.Arguments, p.Meta.ProgressToken)
	case "finish_session":
		s.grillFinishSession(w, sid, rpcID, p.Arguments)
	default:
		respondRPCError(w, rpcID, rpcInvalidParams, "unknown tool: "+p.Name)
	}
}

// grillAskUser posts the question, moves the session to waiting, and blocks on an
// SSE stream until the user's answer arrives or the idle window elapses. A call
// repeating the question the session is already waiting on re-attaches to it rather
// than posting it twice. The answer is returned verbatim as the tool result; on idle
// timeout the session parks and a structured sentinel tells the agent to end its
// turn. An auto-accept session short-circuits all of that for a question that carries
// a recommendation: only the ones needing the user's taste reach them.
func (s *Server) grillAskUser(w http.ResponseWriter, r *http.Request, sid int64, rpcID, args, progressToken json.RawMessage) {
	var a struct {
		Question      string   `json:"question"`
		Options       []string `json:"options"`
		Recommended   string   `json:"recommended"`
		Why           string   `json:"why"`
		AllowFreeText *bool    `json:"allow_free_text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		respondRPCJSON(w, rpcID, mcpToolError("ask_user arguments were not valid JSON"))
		return
	}
	question := strings.TrimSpace(a.Question)
	if question == "" {
		respondRPCJSON(w, rpcID, mcpToolError("question is required and must not be empty"))
		return
	}
	// A queued interjection outranks the question, auto-accept included: the user has
	// already moved the conversation on. The question is neither stored nor posed, so
	// nothing later mistakes it for one the session is waiting on.
	if steer := s.grillTakeInterjections(sid); len(steer) > 0 {
		frame := s.grillPastedAnswer(r.Context(), sid, grillSteerFrame(steer))
		respondRPCJSON(w, rpcID, grillAnswerResult(frame))
		return
	}
	allowFreeText := true
	if a.AllowFreeText != nil {
		allowFreeText = *a.AllowFreeText
	}
	recommended := strings.TrimSpace(a.Recommended)
	payload, _ := json.Marshal(struct {
		Text          string   `json:"text"`
		Options       []string `json:"options,omitempty"`
		Recommended   string   `json:"recommended,omitempty"`
		Why           string   `json:"why,omitempty"`
		AllowFreeText bool     `json:"allow_free_text"`
	}{Text: question, Options: a.Options, Recommended: recommended, Why: strings.TrimSpace(a.Why), AllowFreeText: allowFreeText})

	if recommended != "" && s.grillAutoAccepts(sid) {
		s.grillAutoAnswer(w, sid, rpcID, string(payload), recommended)
		return
	}

	question0, pending := s.grillPendingQuestion(sid, question)
	if !pending {
		stored, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
			Role:    hubstore.GrillRoleAgent,
			Kind:    hubstore.GrillKindQuestion,
			Payload: string(payload),
		})
		if err != nil {
			respondRPCError(w, rpcID, rpcInternalError, "store question: "+err.Error())
			return
		}
		waiting, err := s.stores.Grill().Transition(sid, hubstore.GrillWaiting, "")
		if err != nil {
			respondRPCJSON(w, rpcID, s.grillAskUnavailable(sid))
			return
		}
		question0 = stored
		s.publishGrillMessage(question0)
		s.publishGrillState(waiting)
		s.notifyGrillAwaiting(waiting, question)
	}

	// An AFK pre-grill turn has no user waiting, so a question it could not recommend
	// an answer to parks the session at once and returns the park sentinel as a plain
	// result — the agent ends its turn and the question waits for a live session.
	if s.isPregrill(sid) {
		if parked, err := s.stores.Grill().Transition(sid, hubstore.GrillParked, ""); err == nil {
			s.publishGrillState(parked)
		}
		respondRPCJSON(w, rpcID, grillParkResult())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondRPCError(w, rpcID, rpcInternalError, "streaming unsupported")
		return
	}
	setSSEHeaders(w)
	_, _ = io.WriteString(w, ": open\n\n")
	flusher.Flush()

	sub, ch := s.grillEvents.subscribe()
	defer s.grillEvents.unsubscribe(sub)

	idle := time.NewTimer(grillAskIdleTimeout)
	defer idle.Stop()
	keepalive := time.NewTicker(grillAskKeepalive)
	defer keepalive.Stop()
	progress := 0

	respond := func(result any) {
		_ = writeMCPMessage(w, jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: rpcID, Result: result})
		flusher.Flush()
	}
	answered := func() bool {
		answer, ok := s.grillAnswerAfter(sid, question0.ID)
		if !ok {
			return false
		}
		respond(grillAnswerResult(s.grillPastedAnswer(r.Context(), sid, answer)))
		return true
	}

	if answered() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-idle.C:
			if answered() {
				return
			}
			if parked, err := s.stores.Grill().Transition(sid, hubstore.GrillParked, ""); err == nil {
				s.publishGrillState(parked)
			}
			respond(grillParkResult())
			return
		case <-keepalive.C:
			if answered() {
				return
			}
			progress++
			_ = writeMCPProgress(w, progressToken, progress)
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-ch:
			if ev.SessionID != sid {
				continue
			}
			if answered() {
				return
			}
			if ev.Event == "state" && s.grillSessionEnded(sid) {
				respond(grillEndedResult())
				return
			}
		}
	}
}

// grillAutoAccepts reports whether the session answers its own recommendations. An
// ended session — or a read that fails — answers nothing and falls through to the
// manual path, which returns the stop sentinel rather than auto-answering into a
// transcript the user has already discarded.
func (s *Server) grillAutoAccepts(sid int64) bool {
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil || !found {
		return false
	}
	return sess.AutoAccept && !grillEnded(sess.State)
}

// grillAutoAnswer takes the agent's own recommendation as the answer. The pair still
// lands in the transcript, the answer flagged auto so the audit trail shows who chose,
// but the session never enters waiting: nothing notifies the user and the turn carries
// on. A recommendation matching no offered option is still the answer, verbatim.
func (s *Server) grillAutoAnswer(w http.ResponseWriter, sid int64, rpcID json.RawMessage, question, recommended string) {
	stored, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindQuestion,
		Payload: question,
	})
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "store question: "+err.Error())
		return
	}
	s.publishGrillMessage(stored)

	answer, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleUser,
		Kind:    hubstore.GrillKindAnswer,
		Payload: grillAnswerPayload(recommended, true, false),
	})
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "store auto-accepted answer: "+err.Error())
		return
	}
	s.publishGrillMessage(answer)
	respondRPCJSON(w, rpcID, grillAnswerResult(recommended))
}

// grillFinishSession validates the proposed outcome, stores it as an outcome
// message, and moves the session to finished for the user to review. Validation
// failures come back as a tool error the agent can correct, not a protocol error. A
// queued interjection refuses the finish once — the proposal in hand never saw it —
// and is consumed by that refusal, so finishing again goes through.
func (s *Server) grillFinishSession(w http.ResponseWriter, sid int64, rpcID, args json.RawMessage) {
	if steer := s.grillTakeInterjections(sid); len(steer) > 0 {
		respondRPCJSON(w, rpcID, mcpToolError(grillFinishRefusal(steer)))
		return
	}
	var a struct {
		Disposition         string          `json:"disposition"`
		Title               string          `json:"title"`
		ProposedDescription string          `json:"proposed_description"`
		Findings            string          `json:"findings"`
		Labels              []string        `json:"labels"`
		SubIssues           []grillSubIssue `json:"sub_issues"`
		Summary             string          `json:"summary"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		respondRPCJSON(w, rpcID, mcpToolError("finish_session arguments were not valid JSON"))
		return
	}
	disposition := strings.TrimSpace(a.Disposition)
	if !validGrillDisposition(disposition) {
		respondRPCJSON(w, rpcID, mcpToolError("disposition must be one of: rewrite, split, needs_split, create, research, no_change"))
		return
	}
	proposed := strings.TrimSpace(a.ProposedDescription)
	if needsProposedDescription(disposition) && proposed == "" {
		respondRPCJSON(w, rpcID, mcpToolError("disposition "+disposition+" requires proposed_description"))
		return
	}
	title := strings.TrimSpace(a.Title)
	if disposition == grillDispCreate && title == "" {
		respondRPCJSON(w, rpcID, mcpToolError("disposition create requires a title for the new issue"))
		return
	}
	findings := strings.TrimSpace(a.Findings)
	if disposition == grillDispResearch {
		if findings == "" {
			respondRPCJSON(w, rpcID, mcpToolError("disposition research requires findings: the full Markdown research report"))
			return
		}
		if title == "" {
			respondRPCJSON(w, rpcID, mcpToolError("disposition research requires a title: the report's own title"))
			return
		}
	}
	var subIssues []grillSubIssue
	if disposition == grillDispSplit || (disposition == grillDispCreate && len(a.SubIssues) > 0) {
		var msg string
		subIssues, msg = normalizeSplitSubIssues(a.SubIssues)
		if msg != "" {
			respondRPCJSON(w, rpcID, mcpToolError(msg))
			return
		}
	}
	summary := strings.TrimSpace(a.Summary)
	if summary == "" {
		respondRPCJSON(w, rpcID, mcpToolError("summary is required"))
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
	if !grillFinishable(sess.State) {
		respondRPCJSON(w, rpcID, mcpToolError("this session has already ended and cannot be finished again"))
		return
	}
	outcome, _ := json.Marshal(struct {
		Disposition         string          `json:"disposition"`
		Title               string          `json:"title,omitempty"`
		ProposedDescription string          `json:"proposed_description,omitempty"`
		Findings            string          `json:"findings,omitempty"`
		Labels              []string        `json:"labels,omitempty"`
		SubIssues           []grillSubIssue `json:"sub_issues,omitempty"`
		Summary             string          `json:"summary"`
	}{
		Disposition:         disposition,
		Title:               title,
		ProposedDescription: proposed,
		Findings:            findings,
		Labels:              trimLabels(a.Labels),
		SubIssues:           subIssues,
		Summary:             summary,
	})

	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindOutcome,
		Payload: string(outcome),
	})
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "store outcome: "+err.Error())
		return
	}
	finished, err := s.stores.Grill().Transition(sid, hubstore.GrillFinished, "")
	if err != nil {
		respondRPCJSON(w, rpcID, mcpToolError("could not finish session: "+err.Error()))
		return
	}
	s.publishGrillMessage(msg)
	s.publishGrillState(finished)
	respondRPCJSON(w, rpcID, mcpToolSuccess(
		"Session finished with disposition \""+disposition+"\". The proposed outcome is now awaiting the user's review."))
}

// grillPendingQuestion returns the question the session is already waiting on when
// it is exactly this one. A provider whose MCP client abandons a long ask_user call
// retries it with the same question; re-attaching to the pending one keeps a second
// identical bubble out of the transcript and reopens the wait instead of failing on
// a waiting-to-waiting transition.
func (s *Server) grillPendingQuestion(sid int64, question string) (hubstore.GrillMessage, bool) {
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil || !found || sess.State != hubstore.GrillWaiting {
		return hubstore.GrillMessage{}, false
	}
	last, ok := s.grillTrailingQuestion(sid)
	if !ok || grillMessageText(last.Payload) != question {
		return hubstore.GrillMessage{}, false
	}
	return last, true
}

// grillTrailingQuestion returns the unanswered agent question a blocked, parked or
// stalled session is sitting on. System notices — a model or auto-accept switch — are
// skipped: they land in the transcript without answering anything.
func (s *Server) grillTrailingQuestion(sid int64) (hubstore.GrillMessage, bool) {
	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		return hubstore.GrillMessage{}, false
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == hubstore.GrillRoleSystem {
			continue
		}
		if msgs[i].Role == hubstore.GrillRoleAgent && msgs[i].Kind == hubstore.GrillKindQuestion {
			return msgs[i], true
		}
		break
	}
	return hubstore.GrillMessage{}, false
}

// grillAnswerAfter returns the first user answer stored after afterID, the answer
// to the pending question. The blocked ask_user call also consults it directly so
// a dropped broadcast event or a race with the answer endpoint never loses an
// answer.
func (s *Server) grillAnswerAfter(sid, afterID int64) (string, bool) {
	msgs, err := s.stores.Grill().Messages(sid, afterID)
	if err != nil {
		return "", false
	}
	for _, m := range msgs {
		if m.Role != hubstore.GrillRoleUser || m.Kind != hubstore.GrillKindAnswer {
			continue
		}
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(m.Payload), &p)
		return p.Text, true
	}
	return "", false
}

func (s *Server) grillSessionEnded(sid int64) bool {
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil || !found {
		return true
	}
	return grillEnded(sess.State)
}

func grillEnded(state string) bool {
	switch state {
	case hubstore.GrillAbandoned, hubstore.GrillApplied, hubstore.GrillFinished:
		return true
	}
	return false
}

// grillAskUnavailable explains why a question could not be posed when moving the
// session to waiting failed: an ended session tells the agent to stop, anything
// else is treated as an already-parked session it should not keep asking.
func (s *Server) grillAskUnavailable(sid int64) mcpToolResult {
	if s.grillSessionEnded(sid) {
		return grillEndedResult()
	}
	return grillParkResult()
}

func validGrillDisposition(d string) bool {
	switch d {
	case grillDispRewrite, grillDispSplit, grillDispNeedsSplit, grillDispCreate, grillDispResearch, grillDispNoChange:
		return true
	}
	return false
}

// needsProposedDescription reports whether a disposition must carry a
// proposed_description: the ones that write or file an issue body.
func needsProposedDescription(d string) bool {
	switch d {
	case grillDispRewrite, grillDispSplit, grillDispCreate:
		return true
	}
	return false
}

// normalizeSplitSubIssues trims a split proposal's sub-issues and validates it:
// at least one slice, each with a title and description, and blocked_by indices
// that reference a real sibling and never the slice itself. It returns the
// cleaned slice, or a tool-error message the agent can correct.
func normalizeSplitSubIssues(in []grillSubIssue) ([]grillSubIssue, string) {
	if len(in) == 0 {
		return nil, "disposition split requires a non-empty sub_issues breakdown"
	}
	out := make([]grillSubIssue, len(in))
	for i, sub := range in {
		title := strings.TrimSpace(sub.Title)
		if title == "" {
			return nil, fmt.Sprintf("sub_issue %d is missing a title", i+1)
		}
		desc := strings.TrimSpace(sub.Description)
		if desc == "" {
			return nil, fmt.Sprintf("sub_issue %d (%q) is missing a description", i+1, title)
		}
		blockedBy := make([]int, 0, len(sub.BlockedBy))
		seen := make(map[int]bool, len(sub.BlockedBy))
		for _, dep := range sub.BlockedBy {
			if dep == i {
				return nil, fmt.Sprintf("sub_issue %d (%q) cannot be blocked by itself", i+1, title)
			}
			if dep < 0 || dep >= len(in) {
				return nil, fmt.Sprintf("sub_issue %d (%q) has an out-of-range blocked_by index %d", i+1, title, dep)
			}
			if !seen[dep] {
				seen[dep] = true
				blockedBy = append(blockedBy, dep)
			}
		}
		out[i] = grillSubIssue{Title: title, Description: desc, Labels: trimLabels(sub.Labels), BlockedBy: blockedBy}
	}
	return out, ""
}

// trimLabels drops blank label names, returning nil when none remain so the
// stored proposal carries no empty labels array.
func trimLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func grillFinishable(state string) bool {
	switch state {
	case hubstore.GrillRunning, hubstore.GrillWaiting, hubstore.GrillParked, hubstore.GrillStalled:
		return true
	}
	return false
}

func grillAnswerResult(text string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: text}}}
}

func grillParkResult() mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: "The user has stepped away and did not answer in time. " +
			"End your turn now: do not call ask_user again and do not wait. Your question has been saved and the session " +
			"will resume with the user's answer when they return."}},
		StructuredContent: map[string]any{"status": "parked", "reason": "user_absent"},
	}
}

func grillEndedResult() mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: "This grilling session has ended. Stop now and end your turn; " +
			"do not call any more tools."}},
		StructuredContent: map[string]any{"status": "ended"},
	}
}
