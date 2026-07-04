package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// planReviewFake records the request-changes note and approval driven against the
// session dir, returning a revised PRD from RevisePlan.
type planReviewFake struct {
	fakeAppActions
	reviseDir      string
	reviseNote     string
	approveDir     string
	approved       bool
	publishSkipped bool
	sliceDir       string
}

func (f *planReviewFake) RevisePlan(_ context.Context, dir, note string) (PlanOutcome, error) {
	f.reviseDir, f.reviseNote = dir, note
	return PlanOutcome{Status: "prd", SessionDir: dir, Title: "Widgets v2", Markdown: "# Widgets v2\n\nrevised"}, nil
}

func (f *planReviewFake) ApprovePlan(_ context.Context, dir string) (PublishOutcome, error) {
	f.approveDir, f.approved = dir, true
	if f.publishSkipped {
		return PublishOutcome{}, nil
	}
	return PublishOutcome{Epic: "COD-900", Published: true}, nil
}

func (f *planReviewFake) SlicePlan(_ context.Context, dir string) (PlanOutcome, error) {
	f.sliceDir = dir
	return PlanOutcome{Status: "slices", SessionDir: dir, Epic: "COD-900", Slices: []PlanSlice{{Title: "first slice"}}}, nil
}

// prdModel returns a Plan screen already showing a drafted PRD in the viewport.
func prdModel(t *testing.T, actions Actions) planModel {
	t.Helper()
	m := newPlanModel(context.Background(), actions, DefaultStyles(), 100, 40)
	m.step = planRunning
	m, _ = m.Update(planDoneMsg{out: PlanOutcome{Status: "prd", SessionDir: "/plans/session-1", Title: "Widgets", Markdown: "# Widgets"}})
	if m.step != planPRD {
		t.Fatalf("setup: step = %v, want planPRD", m.step)
	}
	return m
}

// TestPlanReviewVerbs pins the approve and request-changes verbs into the PRD
// view's footer legend.
func TestPlanReviewVerbs(t *testing.T) {
	m := prdModel(t, &planReviewFake{})
	if fh := m.help().footer(); !strings.Contains(fh, "approve") || !strings.Contains(fh, "request changes") {
		t.Errorf("PRD footer missing review verbs: %q", fh)
	}
}

// TestPlanRequestChangesLoop drives the review loop: r opens the change-request
// note, ctrl+d runs a revision against the session dir with the typed note, and the
// revised PRD lands back in the viewport.
func TestPlanRequestChangesLoop(t *testing.T) {
	fake := &planReviewFake{}
	m := prdModel(t, fake)

	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if m.step != planRevise {
		t.Fatalf("after r step = %v, want planRevise", m.step)
	}

	m.changeNote.SetValue("also support CSV")
	m, cmd := m.handleKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.step != planRunning {
		t.Fatalf("after ctrl+d step = %v, want planRunning", m.step)
	}
	if cmd == nil {
		t.Fatal("ctrl+d should return the RevisePlan cmd")
	}
	next := cmd()
	done, ok := next.(planDoneMsg)
	if !ok {
		t.Fatalf("RevisePlan cmd produced %T, want planDoneMsg", next)
	}
	if fake.reviseDir != "/plans/session-1" {
		t.Errorf("RevisePlan dir = %q, want /plans/session-1", fake.reviseDir)
	}
	if fake.reviseNote != "also support CSV" {
		t.Errorf("RevisePlan note = %q, want the typed change request", fake.reviseNote)
	}

	m, _ = m.Update(done)
	if m.step != planPRD {
		t.Fatalf("after revised PRD step = %v, want planPRD", m.step)
	}
	if m.title != "Widgets v2" {
		t.Errorf("revised PRD title = %q, want Widgets v2", m.title)
	}
	if !strings.Contains(m.viewport.View(), "revised") {
		t.Error("viewport did not re-render the revised PRD")
	}
}

// TestPlanRequestChangesEmptyNote keeps the user in the note editor and flags an
// empty change request instead of running a revision.
func TestPlanRequestChangesEmptyNote(t *testing.T) {
	fake := &planReviewFake{}
	m := prdModel(t, fake)
	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'r', Text: "r"})

	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.step != planRevise {
		t.Fatalf("empty note: step = %v, want to stay on planRevise", m.step)
	}
	if !m.badNote {
		t.Error("empty note should set badNote")
	}
	if fake.reviseDir != "" {
		t.Error("RevisePlan should not run on an empty note")
	}
}

// TestPlanRequestChangesCancel drops the note editor back to the PRD view without
// running a revision.
func TestPlanRequestChangesCancel(t *testing.T) {
	fake := &planReviewFake{}
	m := prdModel(t, fake)
	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'r', Text: "r"})

	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.step != planPRD {
		t.Fatalf("esc from note: step = %v, want planPRD", m.step)
	}
	if fake.reviseDir != "" {
		t.Error("esc should not run a revision")
	}
}

// TestPlanApprove approves the drafted PRD: a advances the checkpoint via
// ApprovePlan, and a published approval flows straight into the slice round whose
// drafts land in the review list.
func TestPlanApprove(t *testing.T) {
	fake := &planReviewFake{}
	m := prdModel(t, fake)

	m, cmd := m.handleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("a should return the ApprovePlan cmd")
	}
	next := cmd()
	msg, ok := next.(planApprovedMsg)
	if !ok {
		t.Fatalf("approve cmd produced %T, want planApprovedMsg", next)
	}
	if !fake.approved || fake.approveDir != "/plans/session-1" {
		t.Errorf("ApprovePlan not called with the session dir (approved=%v dir=%q)", fake.approved, fake.approveDir)
	}

	m, cmd = m.Update(msg)
	if m.step != planRunning {
		t.Fatalf("after publish step = %v, want planRunning (the slice round)", m.step)
	}
	if cmd == nil {
		t.Fatal("a published approval should return the SlicePlan cmd")
	}
	done, ok := cmd().(planDoneMsg)
	if !ok || fake.sliceDir != "/plans/session-1" {
		t.Fatalf("SlicePlan not driven against the session dir (msg ok=%v dir=%q)", ok, fake.sliceDir)
	}

	m, _ = m.Update(done)
	if m.step != planSlices {
		t.Fatalf("after slice drafts step = %v, want planSlices", m.step)
	}
	if !strings.Contains(m.body("·"), "first slice") {
		t.Error("review list did not render the drafted slice")
	}
}

// TestPlanApproveWithoutPublish covers the graceful-degradation message: a tracker
// that cannot publish leaves the plan local and says so.
func TestPlanApproveWithoutPublish(t *testing.T) {
	fake := &planReviewFake{}
	fake.publishSkipped = true
	m := prdModel(t, fake)

	_, cmd := m.handleKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	msg := cmd().(planApprovedMsg)
	m, _ = m.Update(msg)

	if m.step != planNote {
		t.Fatalf("after approval step = %v, want planNote", m.step)
	}
	if !strings.Contains(m.note, "approved") || !strings.Contains(m.note, "prd_ready") {
		t.Errorf("note = %q, want a graceful stays-local message", m.note)
	}
}
