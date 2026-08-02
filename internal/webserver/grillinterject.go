package webserver

import (
	"encoding/json"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
)

// grillInterject stores text as an interjection — the user typing while a turn is in
// flight — and puts it on the live stream as an ordinary message. The session stays
// running: the agent picks the queue up at its next tool call.
func (s *Server) grillInterject(sid int64, text string) (hubstore.GrillMessage, error) {
	payload, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	msg, _, err := s.stores.Grill().AppendMessage(sid, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleUser,
		Kind:    hubstore.GrillKindInterjection,
		Payload: string(payload),
	})
	if err != nil {
		return hubstore.GrillMessage{}, err
	}
	s.publishGrillMessage(msg)
	return msg, nil
}

func (s *Server) grillHasInterjection(sid int64) bool {
	pending, err := s.stores.Grill().PendingInterjections(sid)
	if err != nil {
		logger.Verbosef("grill %d: pending interjections: %v", sid, err)
		return false
	}
	return len(pending) > 0
}

// grillTakeInterjections claims every interjection queued since the last delivery, in
// order. The cursor it advances lives on the session row, so a turn that dies with one
// queued hands it to the next turn instead of losing it, and no turn delivers it twice.
func (s *Server) grillTakeInterjections(sid int64) []string {
	msgs, err := s.stores.Grill().ConsumeInterjections(sid)
	if err != nil {
		logger.Verbosef("grill %d: consume interjections: %v", sid, err)
		return nil
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if text := grillMessageText(m.Payload); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// grillSteerFrame wraps claimed interjections for the agent. It is emphatic that they
// answer nothing: a tool result landing where an answer was expected would otherwise
// read as one, and the agent would drop the question it never got to ask.
func grillSteerFrame(texts []string) string {
	return "While you were working the user interjected: " + strings.Join(texts, "\n\n") +
		". Treat this as steering for the interview — it is not an answer to any pending " +
		"question; adjust course, and re-ask your question afterwards only if it still applies."
}

// grillFinishRefusal is the same queue put to an agent about to end the session: the
// proposal it is holding never saw these, so it addresses them and finishes again.
func grillFinishRefusal(texts []string) string {
	return "Before finishing: the user interjected: " + strings.Join(texts, "\n\n") +
		". Address it, then finish again if still appropriate."
}
