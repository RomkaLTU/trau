package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/event"
)

func questionsOutcome() PlanOutcome {
	return PlanOutcome{
		Status:     "questions",
		SessionDir: "/plans/s1",
		Questions: []PlanQuestion{
			{ID: "q1", Text: "scope?", Kind: "single", Options: []PlanOption{{Label: "a"}, {Label: "b"}}, Default: "a"},
		},
	}
}

// drainNotifies runs a command and any batched children, recording notifier
// calls; huh's self-renewing blink is abandoned by runCmd's timeout.
func drainNotifies(cmd tea.Cmd) {
	msg := runCmd(cmd)
	if subs, ok := asCmdSlice(msg); ok {
		for _, c := range subs {
			runCmd(c)
		}
	}
}

// TestPlanQuestionsNotifyWhenUnfocused checks a question round arriving while the
// terminal is unfocused fires the desktop nudge, so a long round never silently
// stalls the session.
func TestPlanQuestionsNotifyWhenUnfocused(t *testing.T) {
	useInteractivePlanMode(t)
	var calls []string
	m := newPlanModel(context.Background(), &planFake{}, DefaultStyles(), 100, 40)
	m.notifier = recordNotifier(&calls)
	m.focused = false
	m.step = planRunning

	m, cmd := m.Update(planDoneMsg{out: questionsOutcome()})
	if m.step != planQuestions {
		t.Fatalf("step = %v, want planQuestions", m.step)
	}
	drainNotifies(cmd)

	if len(calls) != 1 {
		t.Fatalf("got %d notifications, want 1: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], planQuestionsNotify) {
		t.Errorf("notification = %q, want the question-round nudge", calls[0])
	}
}

// TestPlanQuestionsSilentWhenFocused checks the nudge is suppressed while the user
// is at the keyboard — they can already see the round arrive.
func TestPlanQuestionsSilentWhenFocused(t *testing.T) {
	useInteractivePlanMode(t)
	var calls []string
	m := newPlanModel(context.Background(), &planFake{}, DefaultStyles(), 100, 40)
	m.notifier = recordNotifier(&calls)
	m.focused = true
	m.step = planRunning

	_, cmd := m.Update(planDoneMsg{out: questionsOutcome()})
	drainNotifies(cmd)

	if len(calls) != 0 {
		t.Fatalf("focused terminal must not notify, got %v", calls)
	}
}

// TestPlanErrNote checks a blameless provider pause surfaces as a resumable pause,
// while any other failure reads as a plain error — with resume guidance when a
// durable session survives the failure.
func TestPlanErrNote(t *testing.T) {
	paused := planErrNote(errors.New("planning paused: kimi rate/usage limit reached"), true)
	if !strings.HasPrefix(paused, "⏸") || !strings.Contains(paused, "resume") {
		t.Errorf("paused note = %q, want a resumable pause", paused)
	}
	hard := planErrNote(errors.New("parse payload: not json"), false)
	if !strings.HasPrefix(hard, "✗") {
		t.Errorf("error note = %q, want a plain error", hard)
	}
	if strings.Contains(hard, "resume") {
		t.Errorf("error note without a session = %q, must not promise a resume", hard)
	}
	saved := planErrNote(errors.New("publish epic: linear: boom"), true)
	if !strings.Contains(saved, "saved") || !strings.Contains(saved, "resume") {
		t.Errorf("error note with a session = %q, want resume guidance", saved)
	}
}

// TestPlanLiveTailDuringRound checks w attaches to the planning agent's live
// transcript during a round and renders it, the same tail the dashboard gives
// pipeline phases.
func TestPlanLiveTailDuringRound(t *testing.T) {
	src := &fakeTranscriptSource{delta: TranscriptDelta{ID: "1-round", Cols: 80, Rows: 24, Data: []byte("drafting the PRD\n")}}
	SetTranscriptSource(src, "acme")
	t.Cleanup(func() { SetTranscriptSource(nil, "") })

	m := newPlanModel(context.Background(), &planFake{}, DefaultStyles(), 100, 40)
	m.step = planRunning

	m, _ = m.Update(eventMsg{ev: event.Event{Kind: event.KindAgentStart, Fields: map[string]any{
		"transcript_id": "1-round", "cols": 80, "rows": 24,
	}}})
	if m.stream.id != "1-round" {
		t.Fatalf("stream did not pick up the transcript id: %q", m.stream.id)
	}

	m, cmd := m.handleKey(tea.KeyPressMsg{Code: 'w'})
	if !m.stream.attached {
		t.Fatal("w did not attach the live view")
	}
	if cmd == nil {
		t.Fatal("attach should start polling the transcript")
	}

	data, ok := cmd().(streamDataMsg)
	if !ok {
		t.Fatalf("tail cmd produced %T, want streamDataMsg", cmd())
	}
	m, _ = m.Update(data)

	if body := m.body("⣾"); !strings.Contains(body, "drafting the PRD") {
		t.Errorf("attached body did not render the live transcript:\n%s", body)
	}

	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'w'})
	if m.stream.attached {
		t.Error("w again should detach the live view")
	}
}
