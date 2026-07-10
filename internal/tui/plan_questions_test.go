package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// planFake scripts a questions round then a PRD, recording the session dir and
// answers AnswerPlan is driven with.
type planFake struct {
	fakeAppActions
	gotDir     string
	gotAnswers []PlanAnswer
}

func useInteractivePlanMode(t *testing.T) {
	t.Helper()
	t.Setenv("ACCESSIBLE", "")
	t.Setenv("TERM", "xterm-256color")
}

func (f *planFake) StartPlan(context.Context, string) (PlanOutcome, error) {
	return PlanOutcome{
		Status:     "questions",
		SessionDir: "/plans/session-1",
		Questions: []PlanQuestion{
			{ID: "q1", Text: "who is the actor?", Kind: "single", Options: []PlanOption{{Label: "admins"}, {Label: "editors"}}, Default: "admins"},
			{ID: "q2", Text: "name it?", Kind: "text", Default: "Widgets"},
		},
	}, nil
}

func (f *planFake) AnswerPlan(_ context.Context, dir string, answers []PlanAnswer) (PlanOutcome, error) {
	f.gotDir = dir
	f.gotAnswers = answers
	return PlanOutcome{Status: "prd", SessionDir: dir, Title: "Widgets", Markdown: "# Widgets"}, nil
}

// TestPlanQuestionRoundToPRD drives the Plan screen through a full question
// round: a questions outcome renders the huh form, submitting resolves answers
// against the session dir, and the follow-up PRD outcome lands in the viewport.
func TestPlanQuestionRoundToPRD(t *testing.T) {
	useInteractivePlanMode(t)
	fake := &planFake{}
	m := newPlanModel(context.Background(), fake, DefaultStyles(), 100, 40)
	m.idea.SetValue("let users export widgets")

	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.step != planRunning {
		t.Fatalf("after ctrl+d step = %v, want planRunning", m.step)
	}

	out, _ := fake.StartPlan(context.Background(), "x")
	m, cmd := m.Update(planDoneMsg{out: out})
	if m.step != planQuestions {
		t.Fatalf("after questions outcome step = %v, want planQuestions", m.step)
	}
	if m.pform == nil || len(m.pform.bindings) != 2 {
		t.Fatalf("question form not built with 2 bindings")
	}
	if m.sessionDir != "/plans/session-1" {
		t.Errorf("sessionDir = %q, want /plans/session-1", m.sessionDir)
	}
	if cmd == nil {
		t.Error("questions step should return the form Init cmd")
	}
	if !strings.Contains(m.body(""), "who is the actor?") {
		t.Error("question form did not render the first question")
	}

	m, cmd = m.Update(planFormSubmitMsg{})
	if m.step != planRunning {
		t.Fatalf("after submit step = %v, want planRunning", m.step)
	}
	if cmd == nil {
		t.Fatal("submit should return the AnswerPlan cmd")
	}
	next := cmd()
	done, ok := next.(planDoneMsg)
	if !ok {
		t.Fatalf("AnswerPlan cmd produced %T, want planDoneMsg", next)
	}

	if fake.gotDir != "/plans/session-1" {
		t.Errorf("AnswerPlan dir = %q, want /plans/session-1", fake.gotDir)
	}
	if len(fake.gotAnswers) != 2 {
		t.Fatalf("AnswerPlan got %d answers, want 2", len(fake.gotAnswers))
	}
	if got := fake.gotAnswers[0]; got.ID != "q1" || len(got.Values) != 1 || got.Values[0] != "admins" {
		t.Errorf("q1 answer = %+v, want the pre-selected default admins", got)
	}
	if got := fake.gotAnswers[1]; got.ID != "q2" || !got.Skipped || got.Values[0] != "Widgets" {
		t.Errorf("q2 answer = %+v, want skipped default Widgets", got)
	}

	m, _ = m.Update(done)
	if m.step != planPRD {
		t.Fatalf("after PRD outcome step = %v, want planPRD", m.step)
	}
	if m.title != "Widgets" {
		t.Errorf("PRD title = %q, want Widgets", m.title)
	}
}
