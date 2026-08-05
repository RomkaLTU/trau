package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

const grillDefaultProvider = "claude"

// GrillSessionView is one grilling session as the web panel sees it. IssueID is
// omitted for an authoring session anchored to the repo alone; IssueTitle then
// carries the session's seed so the queue can title an issue-less draft. ReportTitle
// is the report's title — the one a rename gave it, else the one its research outcome
// proposed — which the Research page reads in place of the seed; it is absent until
// the session is renamed or finishes with one.
// IssueDestination names where a create-apply filed the anchored issue, so a review
// remounted on a settled session still names the destination it used rather than
// reverting to the picker default, and ApplyWarnings the caveats that apply carried,
// so the same remount raises them again. Provider is the session's locked provider
// and Mode its locked session type; AutoAccept marks a session that answers its own
// recommendations, so the panel can label the answers it never asked for. Applying
// marks an apply the hub is still writing, so a reload and a second tab read it too;
// it lives in memory alone, so a hub restarted mid-apply reports none.
type GrillSessionView struct {
	ID               string   `json:"id"`
	Repo             string   `json:"repo"`
	IssueID          string   `json:"issue_id,omitempty"`
	IssueDestination string   `json:"issue_destination,omitempty"`
	IssueTitle       string   `json:"issue_title,omitempty"`
	ReportTitle      string   `json:"report_title,omitempty"`
	State            string   `json:"state"`
	SessionChain     string   `json:"session_chain,omitempty"`
	Mode             string   `json:"mode"`
	Provider         string   `json:"provider"`
	Model            string   `json:"model,omitempty"`
	ModelOptions     []string `json:"model_options,omitempty"`
	AutoAccept       bool     `json:"auto_accept"`
	Applying         bool     `json:"applying,omitempty"`
	ParkedReason     string   `json:"parked_reason,omitempty"`
	ApplyWarnings    []string `json:"apply_warnings,omitempty"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
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

// GrillActivityView is one thing the agent did mid-turn — reaching for a tool,
// thinking, a tool coming back — so a turn spent working still shows progress. Seq
// numbers a turn's activity on its own count, apart from the deltas, and like a
// delta the frame is never stored. ID ties a tool frame to the result frame that
// closes it, so a client can resolve the row it already drew instead of listing a
// second one. Detail only ever summarizes a call (a path, a query): whole tool
// inputs and their results stay in the child. Text carries the agent's thinking as
// it is written, on the same ephemeral terms as a reply delta — a thinking frame
// without it opens a stretch, one with it grows the stretch already open.
type GrillActivityView struct {
	Seq    int    `json:"seq"`
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
	Text   string `json:"text,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
}

// GrillDefaultsView is what a session of the requested mode started right now would
// run on. Provider availability is mode-dependent, so it is only valid for that mode.
type GrillDefaultsView struct {
	Provider     string                `json:"provider"`
	Model        string                `json:"model,omitempty"`
	ModelOptions []string              `json:"model_options,omitempty"`
	Providers    []GrillProviderOption `json:"providers,omitempty"`
}

// GrillProviderOption is one provider a not-yet-started session can run on. Disabled
// marks a provider the requested mode rules out and Reason says why; Note describes
// what the mode runs on a provider that does support it.
type GrillProviderOption struct {
	Name         string   `json:"name"`
	ModelOptions []string `json:"model_options,omitempty"`
	Disabled     bool     `json:"disabled,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Note         string   `json:"note,omitempty"`
}

// GrillListResponse is the GET /repos/{repo}/grill resource. Tracker is the repo's
// effective tracker provider, so the review UI can name the apply destination
// choice — or withhold it on a repo whose only destination is internal — without
// loading the settings config.
type GrillListResponse struct {
	Repo     string             `json:"repo"`
	Tracker  string             `json:"tracker"`
	Defaults GrillDefaultsView  `json:"defaults"`
	Sessions []GrillSessionView `json:"sessions"`
}

// GrillAwaitingView is one session awaiting the user: the session resource with its
// repo resolved to the registry name the web addresses it by, plus the first line of
// the question it is blocked on so a collapsed dock previews it without loading the
// conversation.
type GrillAwaitingView struct {
	GrillSessionView
	Question string `json:"question,omitempty"`
}

// GrillAwaitingResponse is the GET /grill resource: every session awaiting the user
// across every repo the hub tracks.
type GrillAwaitingResponse struct {
	Sessions []GrillAwaitingView `json:"sessions"`
}

// GrillDetailResponse is the GET /grill/{sid} resource: a session and its full
// conversation.
type GrillDetailResponse struct {
	Session  GrillSessionView   `json:"session"`
	Messages []GrillMessageView `json:"messages"`
}

// GrillCreateRequest is the body of POST /repos/{repo}/grill. IssueID is empty for
// an authoring session anchored to the repo alone; Idea is that session's one-line
// seed, or the focus note an issue-bound interview opens on. Mode, Provider and
// Model are optional; an empty Mode opens an interview. AutoAccept defaults off, so a
// session only answers its own recommendations when the start surface asks for it.
type GrillCreateRequest struct {
	IssueID    string `json:"issue_id"`
	Idea       string `json:"idea"`
	Mode       string `json:"mode"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	AutoAccept bool   `json:"auto_accept"`
}

// GrillAnswerRequest is the body of POST /grill/{sid}/answer.
type GrillAnswerRequest struct {
	Text string `json:"text"`
}

// GrillModelRequest is the body of POST /grill/{sid}/model.
type GrillModelRequest struct {
	Model string `json:"model"`
}

// GrillAutoAcceptRequest is the body of POST /grill/{sid}/auto-accept.
type GrillAutoAcceptRequest struct {
	Enabled bool `json:"enabled"`
}

// GrillTitleRequest is the body of POST /grill/{sid}/title.
type GrillTitleRequest struct {
	Title string `json:"title"`
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
		// The list keeps the mode as asked rather than as validated: an absent one
		// lists every session, where the defaults it rides with always answer for a
		// concrete session type.
		mode := strings.TrimSpace(r.URL.Query().Get("mode"))
		if _, errMsg := grillValidateMode(mode); errMsg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
			return
		}
		s.listGrill(w, repo, strings.TrimSpace(r.URL.Query().Get("state")), mode)
	case http.MethodPost:
		s.createGrill(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listGrill(w http.ResponseWriter, repo registry.Repo, state, mode string) {
	sessions, err := s.stores.Grill().List(repo.Root, state, mode)
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
		Tracker:  s.grillTrackerFor(repo),
		Defaults: s.grillDefaultsView(repo, grillEffectiveMode(mode)),
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
	mode, errMsg := grillValidateMode(req.Mode)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	provider, errMsg := grillValidateProvider(req.Provider, mode)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	// A fix session diagnoses one ticket's failed run, so it is only ever anchored,
	// and it is seeded from the run's dossier before the first turn spawns. A requeue
	// that raced the start has already cleared the fields that dossier is compiled
	// from, which is the one refusal the surface cannot avoid.
	opening := strings.TrimSpace(req.Idea)
	if mode == hubstore.GrillModeFix {
		if issueID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fix mode requires an issue_id"})
			return
		}
		run, ok := s.grillFailedRunFor(repo.Root, issueID)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": issueID + " is no longer in a failed state"})
			return
		}
		if err := s.writeGrillDossier(repo.Root, run); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "compile failure dossier: " + err.Error()})
			return
		}
		if opening == "" {
			opening = grillFixOpeningNote(run)
		}
	}
	if provider == "" {
		provider = s.grillDefaultProviderFor(repo, mode)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = s.grillModelDefaultFor(repo, provider)
	}
	sess, err := s.stores.Grill().Create(hubstore.NewGrillSession{
		Repo:       repo.Root,
		IssueID:    issueID,
		Mode:       mode,
		Provider:   provider,
		Model:      model,
		AutoAccept: req.AutoAccept,
	})
	if err != nil {
		if errors.Is(err, hubstore.ErrGrillActiveSession) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": issueID + " already has an active grill session"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create grill session: " + err.Error()})
		return
	}
	// The opening line — an authoring session's seed idea, an issue grilling's focus
	// note, a fix session's one-line statement of what failed — grounds the first
	// turn's prompt and opens the conversation, so it is stored before the turn spawns.
	if note := opening; note != "" {
		payload, _ := json.Marshal(struct {
			Text string `json:"text"`
		}{Text: note})
		if _, _, err := s.stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
			Role:    hubstore.GrillRoleUser,
			Kind:    hubstore.GrillKindInfo,
			Payload: string(payload),
		}); err != nil {
			logger.Verbosef("grill %d: opening note: %v", sess.ID, err)
		}
	}
	if s.startGrill != nil {
		s.startGrill(r.Context(), sess)
	}
	writeJSON(w, http.StatusCreated, s.grillSessionView(repo.Name, sess))
}

// handleGrillAwaiting lists every session awaiting the user across all repos (GET
// /grill), most recently touched first, plus the ones whose apply the hub is still
// writing — the dock stands for the interviews the user has yet to be free of, and an
// apply it has to wait out is one of them. ?state=running serves the sessions mid-turn
// instead, which is how the repo switcher knows where an agent is at work.
func (s *Server) handleGrillAwaiting(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sessions, err := s.grillFeedSessions(r.URL.Query().Get("state"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sessions = append(sessions, s.grillApplyingSessions()...)
	views := make([]GrillAwaitingView, len(sessions))
	for i, sess := range sessions {
		views[i] = GrillAwaitingView{
			GrillSessionView: s.grillSessionView(s.grillRepoName(sess.Repo), sess),
			Question:         s.grillQuestionPreview(sess),
		}
	}
	writeJSON(w, http.StatusOK, GrillAwaitingResponse{Sessions: views})
}

// grillFeedSessions picks the feed the machine-wide read asked for: only the running
// state has a reader of its own, anything else is the dock's awaiting feed with the
// applies appended.
func (s *Server) grillFeedSessions(state string) ([]hubstore.GrillSession, error) {
	if state == hubstore.GrillRunning {
		return s.stores.Grill().ListRunning()
	}
	sessions, err := s.stores.Grill().ListAwaiting()
	if err != nil {
		return nil, err
	}
	return append(sessions, s.grillApplyingSessions()...), nil
}

// grillRepoName resolves a stored repo root to the registry name the web scopes its
// calls by, degrading to the directory name for a repo the hub no longer tracks.
func (s *Server) grillRepoName(root string) string {
	if repo, ok := s.findRepoByRoot(root); ok {
		return repo.Name
	}
	return filepath.Base(root)
}

// grillQuestionPreview is the one-line preview a collapsed dock shows: what actually
// blocks the session. A parked or stalled session carries its reason (transitions into
// waiting clear it), and only a session still on its question falls back to that.
func (s *Server) grillQuestionPreview(sess hubstore.GrillSession) string {
	body := sess.ParkedReason
	if body == "" {
		body = s.grillNotificationBody(sess)
	}
	line, _, _ := strings.Cut(body, "\n")
	return truncateBody(strings.TrimSpace(line), notificationBodyMax)
}

// handleGrillSession serves one session and its full conversation (GET) and drops a
// research report for good (DELETE).
func (s *Server) handleGrillSession(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getGrillSession(w, r)
	case http.MethodDelete:
		s.deleteGrillSession(w, r)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) getGrillSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return
	}
	msgs, err := s.stores.Grill().Messages(sess.ID, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, GrillDetailResponse{
		Session:  s.grillSessionView("", sess),
		Messages: grillMessageViews(msgs),
	})
}

// deleteGrillSession drops a research report and its transcript for good. Applied
// research is exempt from retention pruning, so this is the only way one leaves the
// store; a session still live is refused rather than killed out from under its turn.
func (s *Server) deleteGrillSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.loadResearchSession(w, r)
	if !ok {
		return
	}
	switch sess.State {
	case hubstore.GrillApplied, hubstore.GrillAbandoned:
	default:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is still live"})
		return
	}
	deleted, err := s.stores.Grill().Delete(sess.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete grill session: " + err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGrillTitle renames a research report (POST). The name the user chose outranks
// the one the outcome proposed from here on, so a follow-up turn never takes it back.
func (s *Server) handleGrillTitle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sess, ok := s.loadResearchSession(w, r)
	if !ok {
		return
	}
	var req GrillTitleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
		return
	}
	updated, found, err := s.stores.Grill().SetTitle(sess.ID, title)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "set title failed"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return
	}
	s.publishGrillState(updated)
	writeJSON(w, http.StatusOK, s.grillSessionView("", updated))
}

// loadResearchSession loads the session a report-management route addresses, answering
// 404 for an interview id: renaming and deleting belong to the reports the research
// page keeps, and the Inbox keeps its own retention semantics.
func (s *Server) loadResearchSession(w http.ResponseWriter, r *http.Request) (hubstore.GrillSession, bool) {
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return hubstore.GrillSession{}, false
	}
	if grillEffectiveMode(sess.Mode) != hubstore.GrillModeResearch {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not a research session"})
		return hubstore.GrillSession{}, false
	}
	return sess, true
}

// handleGrillAnswer appends a user's message and resumes the session (POST). A running
// session queues it as an interjection instead — the turn keeps working and the agent
// reads it at its next tool call. A session that can take neither is refused.
func (s *Server) handleGrillAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return
	}
	interjecting := sess.State == hubstore.GrillRunning
	if !interjecting && !grillAcceptsAnswer(sess.State) {
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
	if interjecting {
		msg, err := s.grillInterject(sess.ID, text)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store interjection: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, GrillAnswerResponse{
			Session: s.grillSessionView("", sess),
			Message: grillMessageView(msg),
		})
		return
	}
	msg, resumed, err := s.grillSubmitAnswer(r.Context(), sess, text, false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, GrillAnswerResponse{
		Session: s.grillSessionView("", resumed),
		Message: grillMessageView(msg),
	})
}

// grillSubmitAnswer stores text as the session's answer and puts it back to running,
// spawning the resume turn a session whose child has gone needs. auto marks an answer
// the hub took from the agent's own recommendation rather than from the user; the
// answer that picks a stopped session back up is marked as steering, so the resume
// turn reaches the agent as a redirect.
func (s *Server) grillSubmitAnswer(
	ctx context.Context,
	sess hubstore.GrillSession,
	text string,
	auto bool,
) (hubstore.GrillMessage, hubstore.GrillSession, error) {
	msg, _, err := s.stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleUser,
		Kind:    hubstore.GrillKindAnswer,
		Payload: grillAnswerPayload(text, auto, grillStopped(sess)),
	})
	if err != nil {
		return hubstore.GrillMessage{}, sess, fmt.Errorf("store answer: %w", err)
	}
	resumed, err := s.stores.Grill().Transition(sess.ID, hubstore.GrillRunning, "")
	if err != nil {
		return hubstore.GrillMessage{}, sess, fmt.Errorf("resume session: %w", err)
	}
	s.publishGrillMessage(msg)
	s.publishGrillState(resumed)
	if s.grillResumeSpawns(sess.ID, sess.State) && s.startGrill != nil {
		s.startGrill(ctx, resumed)
	}
	return msg, resumed, nil
}

// handleGrillAbandon settles a session as abandoned (POST), killing any turn still in
// flight rather than letting it burn until its next tool call. It is idempotent on an
// already-abandoned session and refuses one already applied.
func (s *Server) handleGrillAbandon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
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
	abandoned, err := s.stores.Grill().Transition(sess.ID, hubstore.GrillAbandoned, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.publishGrillState(abandoned)
	s.stopGrillChild(sess.ID)
	writeJSON(w, http.StatusOK, s.grillSessionView("", abandoned))
}

// handleGrillStop kills a session's in-flight turn and parks it for the user to steer
// (POST). The park lands before the child dies so the runner's reconcile, which leaves
// a stopped session alone, never reads the kill as a crash or a wall. An ended session
// is refused, as is one with no turn to stop.
func (s *Server) handleGrillStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return
	}
	sid := sess.ID
	switch sess.State {
	case hubstore.GrillFinished, hubstore.GrillApplied, hubstore.GrillAbandoned:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session no longer accepts a stop"})
		return
	}
	live := s.grillTurnActive != nil && s.grillTurnActive(sid)
	// Two clicks on Stop are one gesture: while the first is still landing — its guard
	// held, or the turn it killed still winding down — the second answers with the
	// session it parked rather than parking and noticing that session twice.
	if (grillStopped(sess) && live) || !s.beginGrillStop(sid) {
		if latest, ok := s.loadGrillSession(w, r); ok {
			writeJSON(w, http.StatusOK, s.grillSessionView("", latest))
		}
		return
	}
	defer s.endGrillStop(sid)

	if sess.State != hubstore.GrillRunning && !live {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no turn is running"})
		return
	}
	stopped := sess
	// A session that idled into parked while its child kept working is already where
	// the stop lands, and the store has no parked-to-parked move: only its child dies.
	if sess.State != hubstore.GrillParked {
		parked, err := s.stores.Grill().Transition(sid, hubstore.GrillParked, grillStoppedReason)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		stopped = parked
		s.publishGrillState(stopped)
	}
	if stopped.AutoAccept {
		updated, found, err := s.stores.Grill().SetAutoAccept(sid, false)
		switch {
		case err != nil:
			logger.Verbosef("grill %d: stop auto-accept: %v", sid, err)
		case found:
			stopped = updated
			s.appendGrillNotice(sid, "Auto-accept recommendations turned off")
			s.publishGrillState(stopped)
		}
	}
	s.appendGrillNotice(sid, "You stopped the agent mid-turn.")
	s.stopGrillChild(sid)
	writeJSON(w, http.StatusOK, s.grillSessionView("", stopped))
}

// beginGrillStop / endGrillStop flag which sessions have a stop in flight, so two
// requests racing out of one double click cannot both park and notice the session.
// The flag lives no longer than the request that takes it; the park it leaves behind
// is what refuses every later stop.
func (s *Server) beginGrillStop(sid int64) bool {
	s.grillStopMu.Lock()
	defer s.grillStopMu.Unlock()
	if s.grillStopping[sid] {
		return false
	}
	s.grillStopping[sid] = true
	return true
}

func (s *Server) endGrillStop(sid int64) {
	s.grillStopMu.Lock()
	delete(s.grillStopping, sid)
	s.grillStopMu.Unlock()
}

// grillStopped reports whether sess is parked because the user stopped its turn — the
// hand-back the runner must not re-settle and the next answer steers from.
func grillStopped(sess hubstore.GrillSession) bool {
	return sess.State == hubstore.GrillParked && sess.ParkedReason == grillStoppedReason
}

// appendGrillNotice records a system notice in the transcript and puts it on the live
// stream. It is bookkeeping the conversation explains itself with, so a failure is
// logged rather than raised.
func (s *Server) appendGrillNotice(sid int64, text string) {
	payload, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleSystem,
		Kind:    hubstore.GrillKindInfo,
		Payload: string(payload),
	})
	if err != nil {
		logger.Verbosef("grill %d: notice: %v", sid, err)
		return
	}
	s.publishGrillMessage(msg)
}

func (s *Server) stopGrillChild(sid int64) {
	if s.stopGrillTurn != nil {
		s.stopGrillTurn(sid)
	}
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
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return
	}
	sid := sess.ID
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

// handleGrillAutoAccept turns a session's auto-accept on or off (POST). The flag is
// read per question, so the switch lands on the next one without runner coordination;
// turning it on while a recommended question is already waiting answers that one too.
// A finished or settled session is refused; the value already in effect is a no-op. A
// change lands as a system notice in the transcript and a state frame on the live
// stream.
func (s *Server) handleGrillAutoAccept(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return
	}
	sid := sess.ID
	switch sess.State {
	case hubstore.GrillFinished, hubstore.GrillApplied, hubstore.GrillAbandoned:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session no longer accepts an auto-accept switch"})
		return
	}
	var req GrillAutoAcceptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.Enabled == sess.AutoAccept {
		writeJSON(w, http.StatusOK, s.grillSessionView("", sess))
		return
	}
	updated, found, err := s.stores.Grill().SetAutoAccept(sid, req.Enabled)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return
	}
	notice := "Auto-accept recommendations turned off"
	if req.Enabled {
		notice = "Auto-accept recommendations turned on"
	}
	payload, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: notice})
	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleSystem,
		Kind:    hubstore.GrillKindInfo,
		Payload: string(payload),
	})
	if err != nil {
		logger.Verbosef("grill %d: auto-accept notice: %v", sid, err)
	} else {
		s.publishGrillMessage(msg)
	}
	s.publishGrillState(updated)
	if req.Enabled {
		updated = s.grillAcceptPending(r.Context(), updated)
	}
	writeJSON(w, http.StatusOK, s.grillSessionView("", updated))
}

// grillAcceptPending answers the question a just-switched-on session is already
// blocked on with the agent's own recommendation, so the flip lands on the question
// the user is looking at rather than only the next one. A question the agent made no
// recommendation on needs the user and is left waiting.
func (s *Server) grillAcceptPending(ctx context.Context, sess hubstore.GrillSession) hubstore.GrillSession {
	if !grillAcceptsAnswer(sess.State) {
		return sess
	}
	question, ok := s.grillTrailingQuestion(sess.ID)
	if !ok {
		return sess
	}
	recommended := grillMessageRecommended(question.Payload)
	if recommended == "" {
		return sess
	}
	_, resumed, err := s.grillSubmitAnswer(ctx, sess, recommended, true)
	if err != nil {
		logger.Verbosef("grill %d: auto-accept pending answer: %v", sess.ID, err)
		return sess
	}
	return resumed
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
	sess, ok := s.loadGrillSession(w, r)
	if !ok {
		return
	}
	sid := sess.ID
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

// publishGrillActivity carries no frame id for the same reason a delta does not:
// activity is progress, not transcript.
func (s *Server) publishGrillActivity(sid int64, act GrillActivityView) {
	s.grillEvents.publish(liveGrillEvent{
		SessionID: sid,
		Event:     "activity",
		Payload:   act,
	})
}

// retireClosedGrill settles the repo's grill sessions whose issue has closed and
// announces each state change to any live stream. It rides every write that can
// close an issue — the tracker sync pass, and the internal-issue transitions a
// hub-tracked repo has instead of a sync — so a failure is logged and retried on the
// next one.
func (s *Server) retireClosedGrill(root string) {
	retired, err := s.stores.Grill().RetireClosed(root)
	if err != nil {
		logger.Verbosef("retire grill sessions %s: %v", root, err)
		return
	}
	for _, sess := range retired {
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
// answer only reaches the agent by resuming. Waiting sessions resume only after the
// child has exited with the question still pending.
func (s *Server) grillResumeSpawns(sid int64, state string) bool {
	switch state {
	case hubstore.GrillParked, hubstore.GrillStalled, hubstore.GrillFinished:
		return true
	case hubstore.GrillWaiting:
		return s.grillTurnActive != nil && !s.grillTurnActive(sid)
	default:
		return false
	}
}

// loadGrillSession reads the session a request names, answering the client itself on
// an unusable id, an unreadable store or an unknown session, and reporting whether
// the handler may carry on.
func (s *Server) loadGrillSession(w http.ResponseWriter, r *http.Request) (hubstore.GrillSession, bool) {
	sid, ok := parseSID(w, r)
	if !ok {
		return hubstore.GrillSession{}, false
	}
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return hubstore.GrillSession{}, false
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return hubstore.GrillSession{}, false
	}
	return sess, true
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
		ID:               strconv.FormatInt(sess.ID, 10),
		Repo:             name,
		IssueID:          sess.IssueID,
		IssueDestination: sess.IssueDestination,
		IssueTitle:       sess.IssueTitle,
		ReportTitle:      sess.ReportTitle,
		State:            sess.State,
		SessionChain:     sess.SessionChain,
		Mode:             grillEffectiveMode(sess.Mode),
		Provider:         grillEffectiveProvider(sess.Provider),
		Model:            s.grillEffectiveModel(sess),
		ModelOptions:     grillModelOptionsFor(sess.Provider),
		AutoAccept:       sess.AutoAccept,
		Applying:         s.grillApplyInFlight(sess.ID),
		ParkedReason:     sess.ParkedReason,
		ApplyWarnings:    sess.ApplyWarnings,
		CreatedAt:        sess.CreatedAt,
		UpdatedAt:        sess.UpdatedAt,
	}
}

func (s *Server) grillDefaultsView(repo registry.Repo, mode string) GrillDefaultsView {
	provider := s.grillDefaultProviderFor(repo, mode)
	return GrillDefaultsView{
		Provider:     provider,
		Model:        s.grillModelDefaultFor(repo, provider),
		ModelOptions: grillModelOptionsFor(provider),
		Providers:    s.grillProviderOptions(repo, provider, mode),
	}
}

// grillProviderOptions is the picker's catalog for one session type. A provider whose
// CLI this machine cannot spawn is left out entirely, but one the mode rules out is
// offered disabled with its reason, so flipping the type explains the loss.
func (s *Server) grillProviderOptions(repo registry.Repo, defaultProvider, mode string) []GrillProviderOption {
	cfg, cfgErr := s.grillConfigFor(repo)
	names := agent.DefaultRegistry().Names()
	out := make([]GrillProviderOption, 0, len(names))
	for _, name := range names {
		if name != grillDefaultProvider && name != defaultProvider && (cfgErr != nil || grillProviderInstallReason(cfg, name) != "") {
			continue
		}
		opt := GrillProviderOption{Name: name, ModelOptions: grillModelOptionsFor(name)}
		if reason := grillProviderModeReason(name, mode); reason != "" {
			opt.Disabled, opt.Reason = true, reason
		} else if mode == hubstore.GrillModeResearch {
			opt.Note = grillResearchNote(name)
		}
		out = append(out, opt)
	}
	return out
}

func grillProviderUnavailableReason(cfg config.Config, provider, mode string) string {
	if reason := grillProviderModeReason(provider, mode); reason != "" {
		return reason
	}
	return grillProviderInstallReason(cfg, provider)
}

// grillProviderModeReason rules kimi out of research: it runs under a synthetic home
// with only the trau-grill MCP server and no permission bypass, so it has no vetted
// way to reach the web.
func grillProviderModeReason(provider, mode string) string {
	if mode == hubstore.GrillModeResearch && grillEffectiveProvider(provider) == "kimi" {
		return "kimi has no web research support in trau yet"
	}
	return ""
}

func grillResearchNote(provider string) string {
	switch grillEffectiveProvider(provider) {
	case "claude":
		return "research uses claude's built-in web search and fetch"
	case "codex":
		return "research enables codex's own web_search tool"
	}
	return ""
}

func grillProviderInstallReason(cfg config.Config, provider string) string {
	provider = grillEffectiveProvider(provider)
	if _, err := exec.LookPath(grillProviderBin(cfg, provider)); err != nil {
		return "the " + provider + " CLI is not installed on this machine"
	}
	if provider == "codex" && !codexGrillSupported(grillProviderBin(cfg, provider)) {
		return "the codex CLI installed on this machine does not support interview sessions"
	}
	return ""
}

func codexGrillSupported(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	execHelp, err := exec.CommandContext(ctx, bin, "exec", "--help").Output()
	if err != nil {
		return false
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mcpHelp, err := exec.CommandContext(ctx, bin, "mcp", "add", "--help").Output()
	if err != nil {
		return false
	}
	return bytes.Contains(execHelp, []byte("resume")) &&
		bytes.Contains(execHelp, []byte("--json")) &&
		bytes.Contains(mcpHelp, []byte("--url"))
}

func grillEffectiveProvider(provider string) string {
	if provider == "" {
		return grillDefaultProvider
	}
	return provider
}

func grillEffectiveMode(mode string) string {
	if mode == "" {
		return hubstore.GrillModeInterview
	}
	return mode
}

func grillValidateMode(mode string) (string, string) {
	switch mode = strings.TrimSpace(mode); mode {
	case "", hubstore.GrillModeInterview:
		return hubstore.GrillModeInterview, ""
	case hubstore.GrillModeResearch, hubstore.GrillModeFix:
		return mode, ""
	}
	return "", fmt.Sprintf(
		"unknown mode %q (expected: %s | %s | %s)",
		mode, hubstore.GrillModeInterview, hubstore.GrillModeResearch, hubstore.GrillModeFix,
	)
}

func grillValidateProvider(provider, mode string) (string, string) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "", ""
	}
	reg := agent.DefaultRegistry()
	if _, known := reg.Lookup(provider); !known {
		return "", fmt.Sprintf("unknown provider %q (expected: %s)", provider, strings.Join(reg.Names(), " | "))
	}
	if reason := grillProviderModeReason(provider, mode); reason != "" {
		return "", reason
	}
	return provider, ""
}

// grillDefaultProviderFor falls back to claude when the repo's configured provider
// cannot run the mode, rather than opening a session that would only park.
func (s *Server) grillDefaultProviderFor(repo registry.Repo, mode string) string {
	cfg, err := s.grillConfigFor(repo)
	if err != nil {
		return grillDefaultProvider
	}
	provider := grillConfiguredProvider(cfg)
	if grillProviderModeReason(provider, mode) != "" {
		return grillDefaultProvider
	}
	return provider
}

func grillConfiguredProvider(cfg config.Config) string {
	provider := strings.TrimSpace(cfg.GrillProvider)
	if provider == "" {
		return grillDefaultProvider
	}
	if _, known := agent.DefaultRegistry().Lookup(provider); known {
		return provider
	}
	return grillDefaultProvider
}

// grillTrackerFor resolves the repo's effective tracker provider for the list
// resource; a config error degrades to empty so the list still serves.
func (s *Server) grillTrackerFor(repo registry.Repo) string {
	cfg, err := s.grillConfigFor(repo)
	if err != nil {
		return ""
	}
	return cfg.EffectiveTrackerProvider()
}

// grillEffectiveModel is the model the session's next turn spawns with: the stored
// choice, or its provider's repo-config fallback.
func (s *Server) grillEffectiveModel(sess hubstore.GrillSession) string {
	if sess.Model != "" {
		return sess.Model
	}
	if r, ok := s.findRepoByRoot(sess.Repo); ok {
		return s.grillModelDefaultFor(r, sess.Provider)
	}
	return ""
}

func (s *Server) grillModelDefaultFor(repo registry.Repo, provider string) string {
	cfg, err := s.grillConfigFor(repo)
	if err != nil {
		return ""
	}
	return grillModelDefault(cfg, provider)
}

func grillModelOptionsFor(provider string) []string {
	switch grillEffectiveProvider(provider) {
	case "codex":
		return config.CodexModels()
	case "kimi":
		return config.KimiModelAliases()
	}
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
