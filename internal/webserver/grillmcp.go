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

// The grilling child reaches the hub over MCP (grilling-prd.md, Mechanism). Each
// session is mounted at its own /grill/{sid}/mcp endpoint, so a child's mcp-config
// points at one session and cannot address another. The transport is Streamable
// HTTP: immediate methods answer with application/json, and the blocking ask_user
// call answers with an SSE stream carrying keepalive/progress notifications until
// the answer arrives.
const (
	jsonrpcVersion     = "2.0"
	mcpProtocolVersion = "2025-06-18"

	rpcParseError     = -32700
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

// Grilling outcome dispositions a child can finish a session with. create files a
// brand-new issue (or epic) from an authoring session with no anchor.
const (
	grillDispRewrite    = "rewrite"
	grillDispSplit      = "split"
	grillDispNeedsSplit = "needs_split"
	grillDispCreate     = "create"
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

var nullID = json.RawMessage("null")

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content           []mcpContent `json:"content"`
	StructuredContent any          `json:"structuredContent,omitempty"`
	IsError           bool         `json:"isError,omitempty"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta"`
}

var grillMCPTools = []mcpTool{
	{
		Name: "ask_user",
		Description: "Ask the user exactly one clarifying question and wait for their answer, which is returned as the tool result. " +
			"Ask one question at a time. If the user has stepped away the call returns a park instruction: end your turn then " +
			"without asking again — the question is saved and the session resumes with their answer when they return.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "The single question to ask the user."},
    "options": {"type": "array", "items": {"type": "string"}, "description": "Optional suggested answers to offer the user."},
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
			"sub_issues breakdown to file it as an epic instead of a single issue), or \"no_change\" (nothing needs writing). " +
			"summary captures the key clarifications reached. Nothing is written to the tracker until the user approves.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "disposition": {"type": "string", "enum": ["rewrite", "split", "needs_split", "create", "no_change"], "description": "The proposed outcome."},
    "title": {"type": "string", "description": "Required when disposition is create: the title of the new issue (or epic) to file."},
    "proposed_description": {"type": "string", "description": "Required when disposition is rewrite (the full replacement issue description), split (the parent rewrite framing the epic goal), or create (the full description of the new issue or epic)."},
    "labels": {"type": "array", "items": {"type": "string"}, "description": "Optional labels for the created issue when disposition is create. A single issue defaults to the ready-for-agent label; an epic parent gets none by default."},
    "sub_issues": {
      "type": "array",
      "description": "Required for split, optional for create: the proposed breakdown, one implementable slice per agent session. Each becomes a child of the parent (the grilled issue for split, the newly created epic for create).",
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
	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondRPCError(w, nullID, rpcParseError, "parse error")
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		s.grillMCPInitialize(w, req)
	case "ping":
		respondRPCJSON(w, req.ID, map[string]any{})
	case "tools/list":
		respondRPCJSON(w, req.ID, map[string]any{"tools": grillMCPTools})
	case "tools/call":
		s.grillMCPToolsCall(w, r, sid, req)
	default:
		respondRPCError(w, req.ID, rpcMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) grillMCPInitialize(w http.ResponseWriter, req jsonrpcRequest) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &p)
	version := p.ProtocolVersion
	if version == "" {
		version = mcpProtocolVersion
	}
	respondRPCJSON(w, req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "trau-grill", "version": s.version},
	})
}

func (s *Server) grillMCPToolsCall(w http.ResponseWriter, r *http.Request, sid int64, req jsonrpcRequest) {
	var p toolsCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		respondRPCError(w, req.ID, rpcInvalidParams, "invalid params")
		return
	}
	switch p.Name {
	case "ask_user":
		s.grillAskUser(w, r, sid, req.ID, p.Arguments, p.Meta.ProgressToken)
	case "finish_session":
		s.grillFinishSession(w, sid, req.ID, p.Arguments)
	default:
		respondRPCError(w, req.ID, rpcInvalidParams, "unknown tool: "+p.Name)
	}
}

// grillAskUser posts the question, moves the session to waiting, and blocks on an
// SSE stream until the user's answer arrives or the idle window elapses. The
// answer is returned verbatim as the tool result; on idle timeout the session
// parks and a structured sentinel tells the agent to end its turn.
func (s *Server) grillAskUser(w http.ResponseWriter, r *http.Request, sid int64, rpcID, args, progressToken json.RawMessage) {
	var a struct {
		Question      string   `json:"question"`
		Options       []string `json:"options"`
		AllowFreeText *bool    `json:"allow_free_text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		respondRPCJSON(w, rpcID, grillToolError("ask_user arguments were not valid JSON"))
		return
	}
	question := strings.TrimSpace(a.Question)
	if question == "" {
		respondRPCJSON(w, rpcID, grillToolError("question is required and must not be empty"))
		return
	}
	allowFreeText := true
	if a.AllowFreeText != nil {
		allowFreeText = *a.AllowFreeText
	}
	payload, _ := json.Marshal(struct {
		Text          string   `json:"text"`
		Options       []string `json:"options,omitempty"`
		AllowFreeText bool     `json:"allow_free_text"`
	}{Text: question, Options: a.Options, AllowFreeText: allowFreeText})

	question0, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
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
	s.publishGrillMessage(question0)
	s.publishGrillState(waiting)

	// An AFK pre-grill turn has no user waiting, so the opening question parks the
	// session at once and returns the park sentinel as a plain result — the agent
	// ends its turn and the question waits for a live session.
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

	if answer, ok := s.grillAnswerAfter(sid, question0.ID); ok {
		respond(grillAnswerResult(answer))
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-idle.C:
			if answer, ok := s.grillAnswerAfter(sid, question0.ID); ok {
				respond(grillAnswerResult(answer))
				return
			}
			if parked, err := s.stores.Grill().Transition(sid, hubstore.GrillParked, ""); err == nil {
				s.publishGrillState(parked)
			}
			respond(grillParkResult())
			return
		case <-keepalive.C:
			if answer, ok := s.grillAnswerAfter(sid, question0.ID); ok {
				respond(grillAnswerResult(answer))
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
			if answer, ok := s.grillAnswerAfter(sid, question0.ID); ok {
				respond(grillAnswerResult(answer))
				return
			}
			if ev.Event == "state" && s.grillSessionEnded(sid) {
				respond(grillEndedResult())
				return
			}
		}
	}
}

// grillFinishSession validates the proposed outcome, stores it as an outcome
// message, and moves the session to finished for the user to review. Validation
// failures come back as a tool error the agent can correct, not a protocol error.
func (s *Server) grillFinishSession(w http.ResponseWriter, sid int64, rpcID, args json.RawMessage) {
	var a struct {
		Disposition         string          `json:"disposition"`
		Title               string          `json:"title"`
		ProposedDescription string          `json:"proposed_description"`
		Labels              []string        `json:"labels"`
		SubIssues           []grillSubIssue `json:"sub_issues"`
		Summary             string          `json:"summary"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		respondRPCJSON(w, rpcID, grillToolError("finish_session arguments were not valid JSON"))
		return
	}
	disposition := strings.TrimSpace(a.Disposition)
	if !validGrillDisposition(disposition) {
		respondRPCJSON(w, rpcID, grillToolError("disposition must be one of: rewrite, split, needs_split, create, no_change"))
		return
	}
	proposed := strings.TrimSpace(a.ProposedDescription)
	if needsProposedDescription(disposition) && proposed == "" {
		respondRPCJSON(w, rpcID, grillToolError("disposition "+disposition+" requires proposed_description"))
		return
	}
	title := strings.TrimSpace(a.Title)
	if disposition == grillDispCreate && title == "" {
		respondRPCJSON(w, rpcID, grillToolError("disposition create requires a title for the new issue"))
		return
	}
	var subIssues []grillSubIssue
	if disposition == grillDispSplit || (disposition == grillDispCreate && len(a.SubIssues) > 0) {
		var msg string
		subIssues, msg = normalizeSplitSubIssues(a.SubIssues)
		if msg != "" {
			respondRPCJSON(w, rpcID, grillToolError(msg))
			return
		}
	}
	summary := strings.TrimSpace(a.Summary)
	if summary == "" {
		respondRPCJSON(w, rpcID, grillToolError("summary is required"))
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
		respondRPCJSON(w, rpcID, grillToolError("this session has already ended and cannot be finished again"))
		return
	}
	outcome, _ := json.Marshal(struct {
		Disposition         string          `json:"disposition"`
		Title               string          `json:"title,omitempty"`
		ProposedDescription string          `json:"proposed_description,omitempty"`
		Labels              []string        `json:"labels,omitempty"`
		SubIssues           []grillSubIssue `json:"sub_issues,omitempty"`
		Summary             string          `json:"summary"`
	}{Disposition: disposition, Title: title, ProposedDescription: proposed, Labels: trimLabels(a.Labels), SubIssues: subIssues, Summary: summary})

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
		respondRPCJSON(w, rpcID, grillToolError("could not finish session: "+err.Error()))
		return
	}
	s.publishGrillMessage(msg)
	s.publishGrillState(finished)
	respondRPCJSON(w, rpcID, grillToolSuccess(
		"Session finished with disposition \""+disposition+"\". The proposed outcome is now awaiting the user's review."))
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
	switch sess.State {
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
	case grillDispRewrite, grillDispSplit, grillDispNeedsSplit, grillDispCreate, grillDispNoChange:
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

func grillToolError(msg string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: msg}}, IsError: true}
}

func grillToolSuccess(msg string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: msg}}}
}

func respondRPCJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: id, Result: result})
}

func respondRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: id, Error: &jsonrpcError{Code: code, Message: msg}})
}

func writeMCPMessage(w io.Writer, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

// writeMCPProgress emits an MCP progress notification, keeping the client's tool
// idle timer fed while ask_user blocks. Progress references the token from the
// tool call; with no token there is nothing to correlate, so it is a no-op and
// the SSE keepalive comment carries the connection alone.
func writeMCPProgress(w io.Writer, token json.RawMessage, progress int) error {
	if len(token) == 0 {
		return nil
	}
	return writeMCPMessage(w, jsonrpcNotification{
		JSONRPC: jsonrpcVersion,
		Method:  "notifications/progress",
		Params: map[string]any{
			"progressToken": token,
			"progress":      progress,
			"message":       "waiting for the user to answer",
		},
	})
}
