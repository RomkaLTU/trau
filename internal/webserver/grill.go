package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// grillProvider is the provider every grilling turn runs on. The runner spawns the
// Claude CLI and reads its stream-json contract, so the choice is fixed here rather
// than stored per session.
const grillProvider = "claude"

// GrillSessionView is one grilling session as the web panel sees it. IssueID is
// omitted for an authoring session anchored to the repo alone; IssueTitle then
// carries the session's seed so the queue can title an issue-less draft. Provider
// is fixed to claude while the runner is; ModelOptions carries the switcher's
// catalog because the inbox never loads the settings config.
type GrillSessionView struct {
	ID           string   `json:"id"`
	Repo         string   `json:"repo"`
	IssueID      string   `json:"issue_id,omitempty"`
	IssueTitle   string   `json:"issue_title,omitempty"`
	State        string   `json:"state"`
	SessionChain string   `json:"session_chain,omitempty"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model,omitempty"`
	ModelOptions []string `json:"model_options,omitempty"`
	ParkedReason string   `json:"parked_reason,omitempty"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// GrillMessageView is one message in a session's conversation. Payload is the
// message's JSON body embedded as-is (a question's text/options, an answer's text,
// an outcome's disposition and proposal).
type GrillMessageView struct {
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// GrillDeltaView is one chunk of the grilling agent's reply as it is written. Seq
// numbers a turn's deltas from one so a client can spot one the broadcaster dropped.
// Deltas are never stored — the turn's message frame stays authoritative.
type GrillDeltaView struct {
	Seq  int    `json:"seq"`
	Text string `json:"text"`
}

// GrillDefaultsView is what an interview started right now would run on: the
// provider, the repo config's model, and the catalog to pick from. It rides on the
// list resource so a start surface can offer the choice before a session exists.
type GrillDefaultsView struct {
	Provider     string   `json:"provider"`
	Model        string   `json:"model,omitempty"`
	ModelOptions []string `json:"model_options,omitempty"`
}

// GrillListResponse is the GET /repos/{repo}/grill resource.
type GrillListResponse struct {
	Repo     string             `json:"repo"`
	Defaults GrillDefaultsView  `json:"defaults"`
	Sessions []GrillSessionView `json:"sessions"`
}

// GrillDetailResponse is the GET /grill/{sid} resource: a session and its full
// conversation.
type GrillDetailResponse struct {
	Session  GrillSessionView   `json:"session"`
	Messages []GrillMessageView `json:"messages"`
}

// GrillCreateRequest is the body of POST /repos/{repo}/grill. IssueID is empty for
// an authoring session anchored to the repo alone; Idea is the one-line seed for
// such a session and is ignored when IssueID is set. Model is optional; an empty
// one resolves to the repo config's grill default at create, so the stored row is
// the source of truth for the session's model.
type GrillCreateRequest struct {
	IssueID string `json:"issue_id"`
	Idea    string `json:"idea"`
	Model   string `json:"model"`
}

// GrillAnswerRequest is the body of POST /grill/{sid}/answer.
type GrillAnswerRequest struct {
	Text string `json:"text"`
}

// GrillModelRequest is the body of POST /grill/{sid}/model.
type GrillModelRequest struct {
	Model string `json:"model"`
}

// GrillAnswerResponse acknowledges an answer with the resulting session state and
// the stored message.
type GrillAnswerResponse struct {
	Session GrillSessionView `json:"session"`
	Message GrillMessageView `json:"message"`
}

// handleRepoGrill lists a repo's grilling sessions (GET) and opens a new one
// (POST). Turn spawning arrives in the runner slice; until a startGrill hook is
// wired a created session simply sits in running. One active session per issue is
// enforced by the store.
func (s *Server) handleRepoGrill(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listGrill(w, repo, strings.TrimSpace(r.URL.Query().Get("state")))
	case http.MethodPost:
		s.createGrill(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listGrill(w http.ResponseWriter, repo registry.Repo, state string) {
	sessions, err := s.stores.Grill().List(repo.Root, state)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	views := make([]GrillSessionView, len(sessions))
	for i, sess := range sessions {
		views[i] = s.grillSessionView(repo.Name, sess)
	}
	writeJSON(w, http.StatusOK, GrillListResponse{
		Repo:     repo.Name,
		Defaults: s.grillDefaultsView(repo),
		Sessions: views,
	})
}

func (s *Server) createGrill(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req GrillCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	issueID := strings.TrimSpace(req.IssueID)
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = s.grillModelDefault(repo)
	}
	sess, err := s.stores.Grill().Create(hubstore.NewGrillSession{
		Repo:    repo.Root,
		IssueID: issueID,
		Model:   model,
	})
	if err != nil {
		if errors.Is(err, hubstore.ErrGrillActiveSession) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": issueID + " already has an active grill session"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create grill session: " + err.Error()})
		return
	}
	// An authoring session's one-line idea seeds the first turn's prompt and opens
	// the conversation, so it is stored before the turn spawns.
	if idea := strings.TrimSpace(req.Idea); issueID == "" && idea != "" {
		payload, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{Text: idea})
		if _, _, err := s.stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
			Role:    hubstore.GrillRoleUser,
			Kind:    hubstore.GrillKindInfo,
			Payload: string(payload),
		}); err != nil {
			logger.Verbosef("grill %d: seed idea: %v", sess.ID, err)
		}
	}
	if s.startGrill != nil {
		s.startGrill(r.Context(), sess)
	}
	writeJSON(w, http.StatusCreated, s.grillSessionView(repo.Name, sess))
}

// handleGrillSession serves one session and its full conversation (GET /grill/{sid}).
func (s *Server) handleGrillSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
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
	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, GrillDetailResponse{
		Session:  s.grillSessionView("", sess),
		Messages: grillMessageViews(msgs),
	})
}

// handleGrillAnswer appends a user's answer and resumes the session (POST). A
// session that cannot take an answer is refused. A parked, stalled or finished
// session has no live child, so its answer fires a --resume turn; a waiting
// session's child is still blocked on the MCP ask_user call and takes the answer
// over that channel.
func (s *Server) handleGrillAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
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
	if !grillAcceptsAnswer(sess.State) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is not awaiting an answer"})
		return
	}
	var req GrillAnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "answer text is required"})
		return
	}
	payload, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleUser,
		Kind:    hubstore.GrillKindAnswer,
		Payload: string(payload),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	prior := sess.State
	resumed, err := s.stores.Grill().Transition(sid, hubstore.GrillRunning, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.publishGrillMessage(msg)
	s.publishGrillState(resumed)
	if grillResumeSpawns(prior) && s.startGrill != nil {
		s.startGrill(r.Context(), resumed)
	}
	writeJSON(w, http.StatusOK, GrillAnswerResponse{
		Session: s.grillSessionView("", resumed),
		Message: grillMessageView(msg),
	})
}

// handleGrillAbandon settles a session as abandoned (POST). It is idempotent on an
// already-abandoned session and refuses one already applied.
func (s *Server) handleGrillAbandon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
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
	switch sess.State {
	case hubstore.GrillAbandoned:
		writeJSON(w, http.StatusOK, s.grillSessionView("", sess))
		return
	case hubstore.GrillApplied:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is already applied"})
		return
	}
	abandoned, err := s.stores.Grill().Transition(sid, hubstore.GrillAbandoned, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.publishGrillState(abandoned)
	writeJSON(w, http.StatusOK, s.grillSessionView("", abandoned))
}

// handleGrillModel switches the Claude model a session's next turn spawns with
// (POST). The runner reads the model at spawn, so an in-flight turn finishes on the
// old one and no runner coordination is needed. A finished or settled session is
// refused; the model already in effect is a no-op. A change lands as a system
// notice in the transcript and a state frame on the live stream.
func (s *Server) handleGrillModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
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
	switch sess.State {
	case hubstore.GrillFinished, hubstore.GrillApplied, hubstore.GrillAbandoned:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session no longer accepts a model switch"})
		return
	}
	var req GrillModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}
	if model == s.grillEffectiveModel(sess) {
		writeJSON(w, http.StatusOK, s.grillSessionView("", sess))
		return
	}
	updated, found, err := s.stores.Grill().SetModel(sid, model)
	if err != nil || !found {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "set model failed"})
		return
	}
	payload, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: "Model switched to " + model})
	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleSystem,
		Kind:    hubstore.GrillKindInfo,
		Payload: string(payload),
	})
	if err != nil {
		logger.Verbosef("grill %d: model notice: %v", sid, err)
	} else {
		s.publishGrillMessage(msg)
	}
	s.publishGrillState(updated)
	writeJSON(w, http.StatusOK, s.grillSessionView("", updated))
}

// handleGrillStream streams a session's messages and state changes over SSE (GET
// /grill/{sid}/stream), same pattern as the transcript stream: backfill from the
// store, then forward live events until the client disconnects.
func (s *Server) handleGrillStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	setSSEHeaders(w)

	sub, ch := s.grillEvents.subscribe()
	defer s.grillEvents.unsubscribe(sub)

	after, _ := parseCursor(r.Header.Get("Last-Event-ID"))
	_ = writeGrillFrame(w, "state", "", s.grillSessionView("", sess))
	msgs, err := s.stores.Grill().Messages(sid, after)
	if err == nil {
		for _, m := range msgs {
			if writeGrillFrame(w, "message", strconv.FormatInt(m.ID, 10), grillMessageView(m)) != nil {
				return
			}
			after = m.ID
		}
	}
	flusher.Flush()
	s.streamGrill(r.Context(), w, flusher, ch, sid, after)
}

// streamGrill forwards live grill events for one session until the client
// disconnects, skipping messages already covered by the backfill. A silent stream
// sends a keepalive comment.
func (s *Server) streamGrill(ctx context.Context, w io.Writer, flusher http.Flusher, ch <-chan liveGrillEvent, sid, lastMsg int64) {
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			if ev.SessionID != sid {
				continue
			}
			if ev.Event == "message" {
				if id, ok := parseCursor(ev.FrameID); ok {
					if id <= lastMsg {
						continue
					}
					lastMsg = id
				}
			}
			if writeGrillFrame(w, ev.Event, ev.FrameID, ev.Payload) != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) publishGrillMessage(msg hubstore.GrillMessage) {
	s.grillEvents.publish(liveGrillEvent{
		SessionID: msg.SessionID,
		Event:     "message",
		FrameID:   strconv.FormatInt(msg.ID, 10),
		Payload:   grillMessageView(msg),
	})
}

// publishGrillDelta carries no frame id: resuming a reconnect from an ephemeral
// delta would skip the stored messages the client actually needs. The broadcaster
// stamps the seq.
func (s *Server) publishGrillDelta(sid int64, text string) {
	s.grillEvents.publish(liveGrillEvent{
		SessionID: sid,
		Event:     "delta",
		Payload:   GrillDeltaView{Text: text},
	})
}

// sweepIdleGrill settles grill sessions idle past the abandon window and announces
// each state change to any live stream. It is best-effort hygiene, so a failure is
// logged and retried on the next prune tick.
func (s *Server) sweepIdleGrill() {
	swept, err := s.stores.Grill().SweepIdle(time.Now().Add(-grillIdleAbandon))
	if err != nil {
		logger.Verbosef("sweep grill sessions: %v", err)
		return
	}
	for _, sess := range swept {
		s.publishGrillState(sess)
	}
}

func (s *Server) publishGrillState(sess hubstore.GrillSession) {
	s.grillEvents.publish(liveGrillEvent{
		SessionID: sess.ID,
		Event:     "state",
		Payload:   s.grillSessionView("", sess),
	})
	// Leaving the awaiting set (answered, thinking, finished, settled) clears the
	// session's needs-you notification. Entering it is recorded at the transition
	// sites, which carry the pending question for the body.
	if !grillAwaiting(sess.State) {
		if err := s.stores.Notifications().ResolveGrillQuestion(sess.ID); err != nil {
			logger.Verbosef("grill %d: resolve notification: %v", sess.ID, err)
		}
	}
}

// grillAcceptsAnswer reports whether a session in state can receive an answer. A
// finished session takes one as a follow-up on its proposed outcome, which reopens
// the session.
func grillAcceptsAnswer(state string) bool {
	switch state {
	case hubstore.GrillWaiting, hubstore.GrillParked, hubstore.GrillStalled, hubstore.GrillFinished:
		return true
	default:
		return false
	}
}

// grillResumeSpawns reports whether answering a session in state must spawn a
// resume turn. A parked, stalled or finished session has no live child, so the
// answer only reaches the agent by resuming; a waiting session's child is still
// blocked on the MCP ask_user call and picks the answer up itself, so spawning
// again would double the turn.
func grillResumeSpawns(state string) bool {
	switch state {
	case hubstore.GrillParked, hubstore.GrillStalled, hubstore.GrillFinished:
		return true
	default:
		return false
	}
}

// parseSID reads the {sid} path segment as a session id, answering 400 on a
// non-numeric value.
func parseSID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	sid, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil || sid <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session id"})
		return 0, false
	}
	return sid, true
}

// grillSessionView maps a stored session onto the API resource. repo names the
// session's repo for the panel; an empty repo keeps the stored root out of a
// session-scoped response, where the panel already knows it. A legacy row's empty
// model resolves through the repo config here, so the panel always sees the model
// the next turn spawns with; a genuinely-unset one stays empty (Claude CLI default).
func (s *Server) grillSessionView(repo string, sess hubstore.GrillSession) GrillSessionView {
	name := repo
	if name == "" {
		name = sess.Repo
	}
	return GrillSessionView{
		ID:           strconv.FormatInt(sess.ID, 10),
		Repo:         name,
		IssueID:      sess.IssueID,
		IssueTitle:   sess.IssueTitle,
		State:        sess.State,
		SessionChain: sess.SessionChain,
		Provider:     grillProvider,
		Model:        s.grillEffectiveModel(sess),
		ModelOptions: grillModelOptions(),
		ParkedReason: sess.ParkedReason,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
	}
}

// grillDefaultsView is the provider and model a session created now would carry.
func (s *Server) grillDefaultsView(repo registry.Repo) GrillDefaultsView {
	return GrillDefaultsView{
		Provider:     grillProvider,
		Model:        s.grillModelDefault(repo),
		ModelOptions: grillModelOptions(),
	}
}

// grillEffectiveModel is the model the session's next turn spawns with: the stored
// choice, or a legacy row's repo-config fallback.
func (s *Server) grillEffectiveModel(sess hubstore.GrillSession) string {
	if sess.Model != "" {
		return sess.Model
	}
	if r, ok := s.findRepoByRoot(sess.Repo); ok {
		return s.grillModelDefault(r)
	}
	return ""
}

// grillModelDefault resolves the model a session with no explicit choice runs on:
// the repo config's GRILL_MODEL, then CLAUDE_MODEL, else empty — the Claude CLI
// default. It mirrors the runner's legacy fallback chain.
func (s *Server) grillModelDefault(repo registry.Repo) string {
	cfg, err := s.grillConfigFor(repo)
	if err != nil {
		return ""
	}
	if m := strings.TrimSpace(cfg.GrillModel); m != "" {
		return m
	}
	return strings.TrimSpace(cfg.ClaudeModel)
}

// grillModelOptions is the Claude model catalog the panel's switcher offers.
func grillModelOptions() []string {
	for _, meta := range config.ProviderTuningMetas() {
		if meta.Name == "claude" {
			return meta.Models
		}
	}
	return nil
}

func grillMessageView(msg hubstore.GrillMessage) GrillMessageView {
	payload := json.RawMessage(msg.Payload)
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	return GrillMessageView{
		ID:        strconv.FormatInt(msg.ID, 10),
		Role:      msg.Role,
		Kind:      msg.Kind,
		Payload:   payload,
		CreatedAt: msg.CreatedAt,
	}
}

func grillMessageViews(msgs []hubstore.GrillMessage) []GrillMessageView {
	out := make([]GrillMessageView, len(msgs))
	for i, m := range msgs {
		out[i] = grillMessageView(m)
	}
	return out
}

// writeGrillFrame writes one SSE frame. A message frame carries the message id so a
// reconnect resumes after it; a state frame carries no id.
func writeGrillFrame(w io.Writer, event, id string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	if id != "" {
		_, err = fmt.Fprintf(w, "event: %s\nid: %s\ndata: %s\n\n", event, id, data)
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}
