package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
)

// grillRoundQuestion is one question of a round, carrying the same per-question fields
// ask_user takes. A round is stored as a single question message whose payload holds
// the ordered list, so the questions the session waits on survive the turn that posed
// them; the answers live beside it in the round-answers table, keyed by position.
type grillRoundQuestion struct {
	Text          string   `json:"text"`
	Options       []string `json:"options,omitempty"`
	Recommended   string   `json:"recommended,omitempty"`
	Why           string   `json:"why,omitempty"`
	AllowFreeText bool     `json:"allow_free_text"`
}

// grillRoundInput is one question as the tool call spells it: "question" rather than
// the stored "text", and a tri-state allow_free_text that defaults to true.
type grillRoundInput struct {
	Question      string   `json:"question"`
	Options       []string `json:"options"`
	Recommended   string   `json:"recommended"`
	Why           string   `json:"why"`
	AllowFreeText *bool    `json:"allow_free_text"`
}

// grillRoundPayloadShape is the stored round question payload. Text is the numbered
// round rendered as one string, so every reader that only knows single questions — the
// notification body, the dock preview, the apply summary — still says something true.
type grillRoundPayloadShape struct {
	Text  string               `json:"text"`
	Round []grillRoundQuestion `json:"round"`
}

// GrillRoundAnswerView is one answered question of a round: the position the question
// holds in the round, the answer, and whether auto-accept gave it.
type GrillRoundAnswerView struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
	Auto  bool   `json:"auto,omitempty"`
}

// GrillRoundView is a round's answer set as it fills in, pushed on the live stream so
// a second tab watching the same session sees each answer land. It carries the round's
// message id rather than a frame id: round progress is not transcript, and a resuming
// client reads the answers off the message itself.
type GrillRoundView struct {
	MessageID string                 `json:"message_id"`
	Answers   []GrillRoundAnswerView `json:"answers"`
}

// GrillRoundAnswerInput is one entry of POST /grill/{sid}/answer's round form.
type GrillRoundAnswerInput struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

var grillAskRoundTool = mcpTool{
	Name: "ask_round",
	Description: "Ask the user a numbered round of independent questions in one exchange and wait for the answers, " +
		"which come back in the order you asked them. This is the default way to interview: put every question whose " +
		"prerequisites are already settled into one round instead of asking them one at a time, and keep ask_user for the " +
		"case where a single question is genuinely all that is open. Every question in a round must stand on its own: never " +
		"include a question whose wording depends on the answer to another question in the same round — ask those in a later " +
		"round, once this round's answers are in. One question per question string, exactly as ask_user requires. If the user " +
		"steps away the call returns a park instruction: end your turn then without asking again — the round is saved, the " +
		"answers already given are kept, and the session resumes when they return.",
	InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "questions": {
      "type": "array",
      "minItems": 1,
      "description": "The round's questions, in the order you want them answered.",
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string", "description": "The single question to ask the user."},
          "options": {"type": "array", "items": {"type": "string"}, "description": "Optional suggested answers to offer the user."},
          "recommended": {"type": "string", "description": "Optional: when you offer options, the one you would choose — repeat that option's text exactly. Omit only for pure-preference questions."},
          "why": {"type": "string", "description": "Optional one-line reason for the recommended option."},
          "allow_free_text": {"type": "boolean", "description": "Whether the user may answer freely instead of picking an option. Defaults to true."}
        },
        "required": ["question"]
      }
    }
  },
  "required": ["questions"]
}`),
}

// grillAskRound poses a round of questions, moves the session to waiting, and blocks on
// an SSE stream until every question has an answer or the idle window elapses. It is
// ask_user's machinery over a set: a queued interjection outranks the round, a call
// re-posing the round the session already waits on re-attaches instead of duplicating
// it, and an idle window that elapses parks the session and returns the park sentinel.
// An auto-accept session answers every question that carries a recommendation before
// the user sees any of them, so a round recommended throughout never reaches them.
func (s *Server) grillAskRound(w http.ResponseWriter, r *http.Request, sid int64, rpcID, args, progressToken json.RawMessage) {
	var a struct {
		Questions []grillRoundInput `json:"questions"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		respondRPCJSON(w, rpcID, mcpToolError("ask_round arguments were not valid JSON"))
		return
	}
	questions, errMsg := normalizeGrillRound(a.Questions)
	if errMsg != "" {
		respondRPCJSON(w, rpcID, mcpToolError(errMsg))
		return
	}
	// A queued interjection outranks the round, auto-accept included, exactly as it
	// outranks a single question: the user has already moved the conversation on, so
	// the round is neither stored nor posed.
	if s.grillSteerInstead(w, r, sid, rpcID) {
		return
	}

	round, reattached := s.grillPendingRound(sid, questions)
	if !reattached {
		stored, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
			Role:    hubstore.GrillRoleAgent,
			Kind:    hubstore.GrillKindQuestion,
			Payload: grillRoundPayload(questions),
		})
		if err != nil {
			respondRPCError(w, rpcID, rpcInternalError, "store round: "+err.Error())
			return
		}
		round = stored
		s.publishGrillMessage(round)
	}
	if s.grillAutoAccepts(sid) {
		s.grillAutoAnswerRound(sid, round.ID, questions)
	}
	answers, err := s.stores.Grill().RoundAnswers(round.ID)
	if err != nil {
		respondRPCError(w, rpcID, rpcInternalError, "read round answers: "+err.Error())
		return
	}
	// Every question answered by the agent's own recommendations: the round is settled
	// before the user ever sees it, so the session never enters waiting and nothing
	// notifies them.
	if len(answers) >= len(questions) {
		if err := s.grillCloseRound(sid, questions, answers); err != nil {
			respondRPCError(w, rpcID, rpcInternalError, err.Error())
			return
		}
		respondRPCJSON(w, rpcID, s.grillRoundResult(r.Context(), sid, questions, answers))
		return
	}
	if !reattached || !s.grillWaitingNow(sid) {
		waiting, err := s.stores.Grill().Transition(sid, hubstore.GrillWaiting, "")
		if err != nil {
			respondRPCJSON(w, rpcID, s.grillAskUnavailable(sid))
			return
		}
		s.publishGrillState(waiting)
		s.notifyGrillAwaiting(waiting, grillRoundNotice(questions, answers))
	}

	// An AFK pre-grill turn has no user waiting, so whatever the recommendations could
	// not settle parks the session at once and the round waits for a live one.
	if s.isPregrill(sid) {
		s.parkGrillIdle(sid)
		respondRPCJSON(w, rpcID, grillRoundParkResult())
		return
	}

	// The round is closed by the answer message the last submission appends, the same
	// signal a blocked ask_user waits on, so the tool never returns ahead of the state
	// change that goes with it.
	s.grillBlockUntilAnswered(w, r, sid, rpcID, progressToken, grillRoundParkResult(), func() (any, bool) {
		text, ok := s.grillAnswerAfter(sid, round.ID)
		if !ok {
			return nil, false
		}
		full, err := s.stores.Grill().RoundAnswers(round.ID)
		if err != nil {
			return nil, false
		}
		// An answer landing on a round still short of answers is the user overtaking it
		// — they stopped the agent and steered instead of finishing the form — so the
		// call returns what they said rather than an answer set with holes in it.
		if len(full) < len(questions) {
			return grillAnswerResult(s.grillPastedAnswer(r.Context(), sid, text)), true
		}
		return s.grillRoundResult(r.Context(), sid, questions, full), true
	})
}

// normalizeGrillRound trims a round and validates it: at least one question, each with
// text of its own. It returns the cleaned questions, or a tool-error message the agent
// can correct.
func normalizeGrillRound(in []grillRoundInput) ([]grillRoundQuestion, string) {
	if len(in) == 0 {
		return nil, "questions is required: a round needs at least one question"
	}
	out := make([]grillRoundQuestion, len(in))
	for i, q := range in {
		text := strings.TrimSpace(q.Question)
		if text == "" {
			return nil, fmt.Sprintf("question %d is missing its question text", i+1)
		}
		allowFreeText := true
		if q.AllowFreeText != nil {
			allowFreeText = *q.AllowFreeText
		}
		out[i] = grillRoundQuestion{
			Text:          text,
			Options:       trimLabels(q.Options),
			Recommended:   strings.TrimSpace(q.Recommended),
			Why:           strings.TrimSpace(q.Why),
			AllowFreeText: allowFreeText,
		}
	}
	return out, ""
}

func grillRoundPayload(questions []grillRoundQuestion) string {
	payload, _ := json.Marshal(grillRoundPayloadShape{
		Text:  grillRoundText(questions),
		Round: questions,
	})
	return string(payload)
}

// grillRoundText renders a round as the numbered list every single-question reader
// falls back to. The first line is the first question, so a one-line preview still
// names something the user recognizes.
func grillRoundText(questions []grillRoundQuestion) string {
	var b strings.Builder
	for i, q := range questions {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. %s", i+1, q.Text)
	}
	return b.String()
}

// grillRoundNotice is the needs-you body for a round: the questions still open, since
// the ones auto-accept already settled are not what the user is being pulled in for.
func grillRoundNotice(questions []grillRoundQuestion, answers []hubstore.GrillRoundAnswer) string {
	answered := grillAnsweredIndexes(answers)
	open := make([]grillRoundQuestion, 0, len(questions))
	for i, q := range questions {
		if !answered[i] {
			open = append(open, q)
		}
	}
	return grillRoundText(open)
}

// grillRoundQuestions reads a stored question payload as a round, reporting whether it
// is one. A single-question payload carries no round and is left to ask_user's readers.
func grillRoundQuestions(payload string) ([]grillRoundQuestion, bool) {
	var p grillRoundPayloadShape
	if json.Unmarshal([]byte(payload), &p) != nil || len(p.Round) == 0 {
		return nil, false
	}
	return p.Round, true
}

// grillPendingRound returns the round message the session is already sitting on when it
// poses exactly these questions and is still short of answers. A provider whose MCP
// client abandons the blocking call retries it with the same round, and a session
// resumed mid-round is told to pose that round again: re-attaching keeps a second copy
// out of the transcript and keeps the answers already given.
func (s *Server) grillPendingRound(sid int64, questions []grillRoundQuestion) (hubstore.GrillMessage, bool) {
	round, posed, ok := s.grillOpenRound(sid)
	if !ok || !grillRoundMatches(posed, questions) {
		return hubstore.GrillMessage{}, false
	}
	return round, true
}

// grillOpenRound returns the round a session is still collecting answers for: the
// question message it trails on, when that message is a round and at least one of its
// questions is unanswered.
func (s *Server) grillOpenRound(sid int64) (hubstore.GrillMessage, []grillRoundQuestion, bool) {
	last, ok := s.grillTrailingQuestion(sid)
	if !ok {
		return hubstore.GrillMessage{}, nil, false
	}
	posed, isRound := grillRoundQuestions(last.Payload)
	if !isRound {
		return hubstore.GrillMessage{}, nil, false
	}
	answers, err := s.stores.Grill().RoundAnswers(last.ID)
	if err != nil || len(answers) >= len(posed) {
		return hubstore.GrillMessage{}, nil, false
	}
	return last, posed, true
}

// grillRoundMatches reports whether two rounds pose the same questions in the same
// order. Only the question text counts: a retry that re-words its own options is still
// the round the user is looking at.
func grillRoundMatches(a, b []grillRoundQuestion) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

func (s *Server) grillWaitingNow(sid int64) bool {
	sess, found, err := s.stores.Grill().Session(sid)
	return err == nil && found && sess.State == hubstore.GrillWaiting
}

// grillAutoAnswerRound answers every question of the round that carries a
// recommendation with that recommendation, flagged auto so the audit trail shows who
// chose. Questions without one need the user's taste and are left for them. A question
// already answered keeps the answer it has, so re-posing a round is idempotent.
func (s *Server) grillAutoAnswerRound(sid, messageID int64, questions []grillRoundQuestion) {
	auto := make([]hubstore.GrillRoundAnswer, 0, len(questions))
	for i, q := range questions {
		if q.Recommended == "" {
			continue
		}
		auto = append(auto, hubstore.GrillRoundAnswer{Index: i, Text: q.Recommended, Auto: true})
	}
	if len(auto) == 0 {
		return
	}
	if err := s.stores.Grill().SaveRoundAnswers(messageID, auto); err != nil {
		logger.Verbosef("grill %d: auto-accept round: %v", sid, err)
		return
	}
	s.publishGrillRound(sid, messageID)
}

// grillCloseRound appends the answer message that settles a round without moving the
// session: the auto-accept path, where the turn carries straight on. The message is
// flagged as the round's own, so the panel reads the answers off the round's cards
// instead of listing them a second time as a bubble.
func (s *Server) grillCloseRound(sid int64, questions []grillRoundQuestion, answers []hubstore.GrillRoundAnswer) error {
	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleUser,
		Kind:    hubstore.GrillKindAnswer,
		Payload: grillRoundAnswerPayload(grillRoundAnswerText(questions, answers), grillAllAuto(answers)),
	})
	if err != nil {
		return fmt.Errorf("store round answers: %w", err)
	}
	s.publishGrillMessage(msg)
	return nil
}

// grillRoundAnswerPayload builds the answer message that closes a round: the ordinary
// answer payload plus the round flag the panel reads.
func grillRoundAnswerPayload(text string, auto bool) string {
	payload, _ := json.Marshal(struct {
		Text  string `json:"text"`
		Auto  bool   `json:"auto,omitempty"`
		Round bool   `json:"round"`
	}{Text: text, Auto: auto, Round: true})
	return string(payload)
}

// grillRoundAnswerText renders a settled round for the agent: every question with the
// answer it got, in the order it was asked. It is both the tool result's body and the
// answer message's text, which is what a resume turn hands the agent.
func grillRoundAnswerText(questions []grillRoundQuestion, answers []hubstore.GrillRoundAnswer) string {
	byIndex := grillAnswerTexts(questions, answers)
	var b strings.Builder
	b.WriteString("Answers to the round you asked:\n")
	for i, q := range questions {
		fmt.Fprintf(&b, "\n%d. %s\nAnswer: %s\n", i+1, q.Text, byIndex[i])
	}
	return strings.TrimRight(b.String(), "\n")
}

// grillRoundResult is the tool result a settled round returns: the whole ordered answer
// set as text, and the same answers structured for an agent that reads them that way.
// Images the user pasted into an answer are materialized here, as they are for a single
// answer, so the child can open a screenshot dropped into any question of the round.
func (s *Server) grillRoundResult(
	ctx context.Context,
	sid int64,
	questions []grillRoundQuestion,
	answers []hubstore.GrillRoundAnswer,
) mcpToolResult {
	body := s.grillPastedAnswer(ctx, sid, grillRoundAnswerText(questions, answers))
	return mcpToolResult{
		Content:           []mcpContent{{Type: "text", Text: body}},
		StructuredContent: map[string]any{"answers": grillAnswerTexts(questions, answers)},
	}
}

// grillAnswerTexts lays the answers out by question position, so a round read back is
// always as long as the round asked even if a position somehow carries nothing.
func grillAnswerTexts(questions []grillRoundQuestion, answers []hubstore.GrillRoundAnswer) []string {
	out := make([]string, len(questions))
	for _, a := range answers {
		if a.Index >= 0 && a.Index < len(out) {
			out[a.Index] = a.Text
		}
	}
	return out
}

func grillAnsweredIndexes(answers []hubstore.GrillRoundAnswer) map[int]bool {
	out := make(map[int]bool, len(answers))
	for _, a := range answers {
		out[a.Index] = true
	}
	return out
}

// grillAllAuto reports whether every answer of a round came from the agent's own
// recommendation — the round the user never saw.
func grillAllAuto(answers []hubstore.GrillRoundAnswer) bool {
	for _, a := range answers {
		if !a.Auto {
			return false
		}
	}
	return len(answers) > 0
}

func grillRoundParkResult() mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: "The user has stepped away and did not finish your round in time. " +
			"End your turn now: do not ask again and do not wait. The answers they already gave are saved; when the session " +
			"resumes, pose the same round again with ask_round — the answered questions keep their answers and only the " +
			"unanswered ones reach the user."}},
		StructuredContent: map[string]any{"status": "parked", "reason": "user_absent"},
	}
}

// answerGrillRound records the answers a round submission carries. A submission that
// leaves questions open keeps the round open with them: the answers given are kept, and
// a session with no child on it resumes so the agent re-poses the remainder. The
// submission that completes the round settles it like any other answer.
func (s *Server) answerGrillRound(w http.ResponseWriter, r *http.Request, sess hubstore.GrillSession, in []GrillRoundAnswerInput) {
	round, questions, ok := s.grillOpenRound(sess.ID)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is not waiting on a round"})
		return
	}
	stored, err := s.stores.Grill().RoundAnswers(round.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	answered := grillAnsweredIndexes(stored)
	fresh := make([]hubstore.GrillRoundAnswer, 0, len(in))
	for _, a := range in {
		if a.Index < 0 || a.Index >= len(questions) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "answer index " + strconv.Itoa(a.Index) + " is outside the round",
			})
			return
		}
		text := strings.TrimSpace(a.Text)
		if text == "" || answered[a.Index] {
			continue
		}
		answered[a.Index] = true
		fresh = append(fresh, hubstore.GrillRoundAnswer{Index: a.Index, Text: text})
	}
	if len(fresh) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "answer text is required"})
		return
	}
	if err := s.stores.Grill().SaveRoundAnswers(round.ID, fresh); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store round answers: " + err.Error()})
		return
	}
	s.publishGrillRound(sess.ID, round.ID)
	full, err := s.stores.Grill().RoundAnswers(round.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(full) < len(questions) {
		writeJSON(w, http.StatusOK, GrillAnswerResponse{Session: s.grillSessionView("", s.grillResumeRound(r.Context(), sess))})
		return
	}
	msg, resumed, err := s.grillSubmitAnswer(r.Context(), sess, grillRoundAnswerText(questions, full), false, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	view := grillMessageView(msg)
	writeJSON(w, http.StatusOK, GrillAnswerResponse{Session: s.grillSessionView("", resumed), Message: &view})
}

// grillResumeRound picks a session back up on a round that is still short of answers.
// A session whose child is still blocked on the round is left alone — the block is
// already collecting them — but one that parked or crashed has to run again for the
// agent to re-pose what is left.
func (s *Server) grillResumeRound(ctx context.Context, sess hubstore.GrillSession) hubstore.GrillSession {
	if !s.grillResumeSpawns(sess.ID, sess.State) {
		return sess
	}
	resumed, err := s.stores.Grill().Transition(sess.ID, hubstore.GrillRunning, "")
	if err != nil {
		logger.Verbosef("grill %d: resume round: %v", sess.ID, err)
		return sess
	}
	s.publishGrillState(resumed)
	if s.startGrill != nil {
		s.startGrill(ctx, resumed)
	}
	return resumed
}

// grillAcceptPendingRound answers the recommendation-bearing questions of the round a
// just-switched-on session is already blocked on, so the flip lands on the round the
// user is looking at rather than only the next one. A round still holding questions the
// agent made no recommendation on keeps waiting on those alone.
func (s *Server) grillAcceptPendingRound(
	ctx context.Context,
	sess hubstore.GrillSession,
	messageID int64,
	questions []grillRoundQuestion,
) hubstore.GrillSession {
	s.grillAutoAnswerRound(sess.ID, messageID, questions)
	answers, err := s.stores.Grill().RoundAnswers(messageID)
	if err != nil || len(answers) < len(questions) {
		return sess
	}
	_, resumed, err := s.grillSubmitAnswer(ctx, sess, grillRoundAnswerText(questions, answers), grillAllAuto(answers), true)
	if err != nil {
		logger.Verbosef("grill %d: auto-accept pending round: %v", sess.ID, err)
		return sess
	}
	return resumed
}

// grillRoundResumePrompt is the resume-turn prompt for a session picked back up while a
// round is still open: what the user has answered so far, and the instruction to pose
// the same round again so the hub can hand the rest of it to them.
func (s *Server) grillRoundResumePrompt(sid int64) (string, bool) {
	round, questions, ok := s.grillOpenRound(sid)
	if !ok {
		return "", false
	}
	answers, err := s.stores.Grill().RoundAnswers(round.ID)
	if err != nil || len(answers) == 0 {
		return "", false
	}
	texts := grillAnswerTexts(questions, answers)
	answered := grillAnsweredIndexes(answers)
	var b strings.Builder
	b.WriteString("The user answered part of the round you asked:\n")
	for i, q := range questions {
		if answered[i] {
			fmt.Fprintf(&b, "\n%d. %s\nAnswer: %s\n", i+1, q.Text, texts[i])
		}
	}
	b.WriteString("\nStill unanswered:\n")
	for i, q := range questions {
		if !answered[i] {
			fmt.Fprintf(&b, "\n%d. %s\n", i+1, q.Text)
		}
	}
	b.WriteString("\nPose the same round again with ask_round — the answered questions keep their answers " +
		"and only the unanswered ones reach the user.")
	return b.String(), true
}

// publishGrillRound puts a round's answer set on the live stream. It carries no frame
// id: a client resuming from a message cursor reads the answers off the round message
// itself, and a frame id here would move that cursor past transcript it still needs.
func (s *Server) publishGrillRound(sid, messageID int64) {
	answers, err := s.stores.Grill().RoundAnswers(messageID)
	if err != nil {
		logger.Verbosef("grill %d: read round answers: %v", sid, err)
		return
	}
	s.grillEvents.publish(liveGrillEvent{
		SessionID: sid,
		Event:     "round",
		Payload:   GrillRoundView{MessageID: strconv.FormatInt(messageID, 10), Answers: grillRoundAnswerViews(answers)},
	})
}

func grillRoundAnswerViews(answers []hubstore.GrillRoundAnswer) []GrillRoundAnswerView {
	out := make([]GrillRoundAnswerView, len(answers))
	for i, a := range answers {
		out[i] = GrillRoundAnswerView{Index: a.Index, Text: a.Text, Auto: a.Auto}
	}
	return out
}
