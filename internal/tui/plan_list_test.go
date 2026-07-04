package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// planListFake serves a scripted session list and records the dirs Resume and
// Abort are driven with. Aborting mutates the backing list so a reload reflects
// the terminal session, mirroring the durable store.
type planListFake struct {
	fakeAppActions
	sessions  []PlanSession
	resumeOut PlanOutcome
	resumeDir string
	abortDir  string
}

func (f *planListFake) ListPlans() []PlanSession {
	out := make([]PlanSession, len(f.sessions))
	copy(out, f.sessions)
	return out
}

func (f *planListFake) ResumePlan(_ context.Context, dir string) (PlanOutcome, error) {
	f.resumeDir = dir
	out := f.resumeOut
	out.SessionDir = dir
	return out, nil
}

func (f *planListFake) AbortPlan(_ context.Context, dir string) error {
	f.abortDir = dir
	for i := range f.sessions {
		if f.sessions[i].Dir == dir {
			f.sessions[i].Phase = "aborted"
			f.sessions[i].Resumable = false
		}
	}
	return nil
}

func twoSessionFake() *planListFake {
	return &planListFake{
		sessions: []PlanSession{
			{Dir: "/plans/s2", Phase: "questions", Idea: "export widgets", Resumable: true},
			{Dir: "/plans/s1", Phase: "aborted", Title: "Old draft", Resumable: false},
		},
	}
}

// TestPlanListEntry opens the Plan screen with saved sessions and lands on the
// list rather than the idea entry, rendering each session's state.
func TestPlanListEntry(t *testing.T) {
	m := newPlanModel(context.Background(), twoSessionFake(), DefaultStyles(), 100, 40)
	if m.step != planList {
		t.Fatalf("step = %v, want planList when sessions exist", m.step)
	}
	body := m.body("")
	if !strings.Contains(body, "questions") || !strings.Contains(body, "export widgets") {
		t.Errorf("list body missing the in-flight session: %q", body)
	}
	if !strings.Contains(body, "aborted") || !strings.Contains(body, "Old draft") {
		t.Errorf("list body missing the terminal session: %q", body)
	}
}

// TestPlanListResumeQuestions resumes the selected in-flight session; a questions
// outcome rebuilds the huh form against that session's dir.
func TestPlanListResumeQuestions(t *testing.T) {
	fake := twoSessionFake()
	fake.resumeOut = PlanOutcome{
		Status:    "questions",
		Questions: []PlanQuestion{{ID: "q1", Text: "who is the actor?", Kind: "single", Options: []PlanOption{{Label: "admins"}}}},
	}
	m := newPlanModel(context.Background(), fake, DefaultStyles(), 100, 40)

	m, cmd := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != planRunning {
		t.Fatalf("after enter step = %v, want planRunning", m.step)
	}
	if m.sessionDir != "/plans/s2" {
		t.Errorf("sessionDir = %q, want the resumed session /plans/s2", m.sessionDir)
	}
	if cmd == nil {
		t.Fatal("enter should return the ResumePlan cmd")
	}
	done, ok := cmd().(planDoneMsg)
	if !ok {
		t.Fatalf("resume cmd produced %T, want planDoneMsg", done)
	}
	if fake.resumeDir != "/plans/s2" {
		t.Errorf("ResumePlan dir = %q, want /plans/s2", fake.resumeDir)
	}

	m, _ = m.Update(done)
	if m.step != planQuestions {
		t.Fatalf("after resumed questions step = %v, want planQuestions", m.step)
	}
	if m.pform == nil || !strings.Contains(m.body(""), "who is the actor?") {
		t.Error("resumed question form not rebuilt")
	}
}

// TestPlanListAbort aborts the selected in-flight session and reloads the list
// with that session now terminal.
func TestPlanListAbort(t *testing.T) {
	fake := twoSessionFake()
	m := newPlanModel(context.Background(), fake, DefaultStyles(), 100, 40)

	m, cmd := m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd == nil {
		t.Fatal("x should return the AbortPlan cmd")
	}
	done, ok := cmd().(planAbortDoneMsg)
	if !ok {
		t.Fatalf("abort cmd produced %T, want planAbortDoneMsg", done)
	}
	if fake.abortDir != "/plans/s2" {
		t.Errorf("AbortPlan dir = %q, want /plans/s2", fake.abortDir)
	}

	m, _ = m.Update(done)
	if m.step != planList {
		t.Fatalf("after abort step = %v, want planList", m.step)
	}
	if m.sessions[0].Resumable {
		t.Error("aborted session should reload as non-resumable")
	}
}

// TestPlanListAbortIgnoredOnTerminal keeps a terminal session untouched — x only
// aborts what is still resumable.
func TestPlanListAbortIgnoredOnTerminal(t *testing.T) {
	fake := twoSessionFake()
	m := newPlanModel(context.Background(), fake, DefaultStyles(), 100, 40)
	m.listCursor = 1 // the aborted session

	m, cmd := m.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd != nil {
		t.Error("x on a terminal session should do nothing")
	}
	if fake.abortDir != "" {
		t.Errorf("AbortPlan should not run on a terminal session (dir=%q)", fake.abortDir)
	}
	if m.step != planList {
		t.Errorf("step = %v, want to stay on planList", m.step)
	}
}

// TestPlanListNewIdea drops from the list into the idea entry, and esc returns to
// the list rather than the menu.
func TestPlanListNewIdea(t *testing.T) {
	m := newPlanModel(context.Background(), twoSessionFake(), DefaultStyles(), 100, 40)

	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.step != planInput {
		t.Fatalf("after n step = %v, want planInput", m.step)
	}

	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.step != planList {
		t.Fatalf("esc from a listed session's new idea = %v, want planList", m.step)
	}
	if m.Cancelled() {
		t.Error("esc should return to the list, not cancel the screen")
	}
}

// TestPlanListNavAndBack moves the cursor and backs out to the menu.
func TestPlanListNavAndBack(t *testing.T) {
	m := newPlanModel(context.Background(), twoSessionFake(), DefaultStyles(), 100, 40)

	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.listCursor != 1 {
		t.Errorf("cursor = %d, want 1 after down", m.listCursor)
	}
	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.listCursor != 1 {
		t.Errorf("cursor = %d, want clamped at 1", m.listCursor)
	}
	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if m.listCursor != 0 {
		t.Errorf("cursor = %d, want 0 after up", m.listCursor)
	}

	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.Cancelled() {
		t.Error("esc from the list should cancel back to the menu")
	}
}
