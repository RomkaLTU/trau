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

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// GrillSessionView is one grilling session as the web panel sees it. IssueID is
// omitted for an authoring session anchored to the repo alone.
type GrillSessionView struct {
	ID           string `json:"id"`
	Repo         string `json:"repo"`
	IssueID      string `json:"issue_id,omitempty"`
	State        string `json:"state"`
	SessionChain string `json:"session_chain,omitempty"`
	Model        string `json:"model,omitempty"`
	ParkedReason string `json:"parked_reason,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
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

// GrillListResponse is the GET /repos/{repo}/grill resource.
type GrillListResponse struct {
	Repo     string             `json:"repo"`
	Sessions []GrillSessionView `json:"sessions"`
}

// GrillDetailResponse is the GET /grill/{sid} resource: a session and its full
// conversation.
type GrillDetailResponse struct {
	Session  GrillSessionView   `json:"session"`
	Messages []GrillMessageView `json:"messages"`
}

// GrillCreateRequest is the body of POST /repos/{repo}/grill. IssueID is empty for
// an authoring session anchored to the repo alone; Model is optional and the runner
// resolves the default when it spawns the turn.
type GrillCreateRequest struct {
	IssueID string `json:"issue_id"`
	Model   string `json:"model"`
}

// GrillAnswerRequest is the body of POST /grill/{sid}/answer.
type GrillAnswerRequest struct {
	Text string `json:"text"`
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
		views[i] = grillSessionView(repo.Name, sess)
	}
	writeJSON(w, http.StatusOK, GrillListResponse{Repo: repo.Name, Sessions: views})
}

func (s *Server) createGrill(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req GrillCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	issueID := strings.TrimSpace(req.IssueID)
	sess, err := s.stores.Grill().Create(hubstore.NewGrillSession{
		Repo:    repo.Root,
		IssueID: issueID,
		Model:   strings.TrimSpace(req.Model),
	})
	if err != nil {
		if errors.Is(err, hubstore.ErrGrillActiveSession) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": issueID + " already has an active grill session"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create grill session: " + err.Error()})
		return
	}
	if s.startGrill != nil {
		s.startGrill(r.Context(), sess)
	}
	writeJSON(w, http.StatusCreated, grillSessionView(repo.Name, sess))
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
		Session:  grillSessionView("", sess),
		Messages: grillMessageViews(msgs),
	})
}

// handleGrillAnswer appends a user's answer and resumes the session (POST). A
// session that is not awaiting an answer is refused. Moving to running is the
// resume signal the runner slice acts on.
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
	if !grillAwaitingAnswer(sess.State) {
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
	resumed, err := s.stores.Grill().Transition(sid, hubstore.GrillRunning, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.publishGrillMessage(msg)
	s.publishGrillState(resumed)
	writeJSON(w, http.StatusOK, GrillAnswerResponse{
		Session: grillSessionView("", resumed),
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
		writeJSON(w, http.StatusOK, grillSessionView("", sess))
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
	writeJSON(w, http.StatusOK, grillSessionView("", abandoned))
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
	_ = writeGrillFrame(w, "state", "", grillSessionView("", sess))
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
		Payload:   grillSessionView("", sess),
	})
}

// grillAwaitingAnswer reports whether a session in state can receive an answer.
func grillAwaitingAnswer(state string) bool {
	switch state {
	case hubstore.GrillWaiting, hubstore.GrillParked, hubstore.GrillStalled:
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
// session-scoped response, where the panel already knows it.
func grillSessionView(repo string, sess hubstore.GrillSession) GrillSessionView {
	name := repo
	if name == "" {
		name = sess.Repo
	}
	return GrillSessionView{
		ID:           strconv.FormatInt(sess.ID, 10),
		Repo:         name,
		IssueID:      sess.IssueID,
		State:        sess.State,
		SessionChain: sess.SessionChain,
		Model:        sess.Model,
		ParkedReason: sess.ParkedReason,
		CreatedAt:    sess.CreatedAt,
		UpdatedAt:    sess.UpdatedAt,
	}
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
