package tui

import (
	"context"
	"strings"
	"testing"
)

// TestRenderMenuShowsResume confirms the assembled landing card surfaces the
// resumable ticket by name when one exists, and stays clean when none does.
func TestRenderMenuShowsResume(t *testing.T) {
	with := appModel{
		styles: DefaultStyles(),
		width:  80, height: 40,
		info: MenuInfo{Resume: ResumeTarget{ID: "COD-498", Phase: "handoff", Title: "Enrich"}},
	}
	if out := with.renderMenu(); !strings.Contains(out, "COD-498") || !strings.Contains(out, "resumes from") {
		t.Errorf("renderMenu must name the resume target, got:\n%s", out)
	}

	without := appModel{styles: DefaultStyles(), width: 80, height: 40, info: MenuInfo{}}
	if out := without.renderMenu(); strings.Contains(out, "resumes from") {
		t.Errorf("renderMenu must not show a resume callout when none is pending, got:\n%s", out)
	}
}

// TestRunOnceConfirmShowsResume confirms the run-once entry screen offers the
// in-flight ticket so a user can continue it without knowing its id.
func TestRunOnceConfirmShowsResume(t *testing.T) {
	info := MenuInfo{Resume: ResumeTarget{ID: "COD-498", Phase: "handoff", Title: "Enrich"}}
	m := newRunOnceModel(context.Background(), nil, DefaultStyles(), info, 80, 24)
	out := m.renderConfirm()
	if !strings.Contains(out, "COD-498") || !strings.Contains(out, "enter") {
		t.Errorf("run-once confirm must offer the resume, got:\n%s", out)
	}
}

// TestResumeTargetActive covers the predicate that gates every resume callout:
// a zero value is inactive, a populated id is active.
func TestResumeTargetActive(t *testing.T) {
	if (ResumeTarget{}).Active() {
		t.Error("zero ResumeTarget must be inactive")
	}
	if !(ResumeTarget{ID: "COD-498"}).Active() {
		t.Error("ResumeTarget with an id must be active")
	}
}

// TestResumeTargetLine checks the shared callout string: it always names the id
// and phase, appends the title only when present, and falls back when the phase
// is unknown.
func TestResumeTargetLine(t *testing.T) {
	full := ResumeTarget{ID: "COD-498", Phase: "handoff", Title: "Enrich conversations"}.Line()
	for _, want := range []string{"COD-498", "handoff", "Enrich conversations"} {
		if !strings.Contains(full, want) {
			t.Errorf("Line() = %q, want it to contain %q", full, want)
		}
	}

	noTitle := ResumeTarget{ID: "COD-498", Phase: "handoff"}.Line()
	if strings.Contains(noTitle, "—") {
		t.Errorf("Line() without a title must omit the em-dash separator, got %q", noTitle)
	}

	noPhase := ResumeTarget{ID: "COD-498"}.Line()
	if !strings.Contains(noPhase, "where it left off") {
		t.Errorf("Line() without a phase must fall back, got %q", noPhase)
	}
}

// TestRunOnceListRowsPinsResume guards the run-once resume parity: the resumable
// ticket is listed first and labeled, and it is de-duped out of the ready queue so
// a user can pick it directly (index 0) without it appearing twice.
func TestRunOnceListRowsPinsResume(t *testing.T) {
	m := runOnceModel{
		info: MenuInfo{Resume: ResumeTarget{ID: "COD-498", Phase: "handoff", Title: "Enrich"}},
		eligible: []ListedTicket{
			{ID: "COD-500", Title: "ready one", State: "Todo"},
			{ID: "COD-498", Title: "same as resume", State: "Todo"},
		},
	}

	rows := m.listRows()
	if len(rows) != 2 {
		t.Fatalf("listRows() len = %d, want 2 (resume + one ready, resume de-duped)", len(rows))
	}
	if rows[0].ID != "COD-498" {
		t.Errorf("resume must be pinned first, got %q", rows[0].ID)
	}
	if !strings.Contains(rows[0].State, "resume") {
		t.Errorf("resume row must be labeled, got state %q", rows[0].State)
	}
	if rows[1].ID != "COD-500" {
		t.Errorf("ready ticket must follow the resume, got %q", rows[1].ID)
	}
}

// TestRunOnceListRowsNoResume keeps the no-resume path identical to the eligible
// queue (no synthetic row, no reordering).
func TestRunOnceListRowsNoResume(t *testing.T) {
	m := runOnceModel{
		eligible: []ListedTicket{{ID: "COD-500", Title: "ready one", State: "Todo"}},
	}
	rows := m.listRows()
	if len(rows) != 1 || rows[0].ID != "COD-500" {
		t.Fatalf("listRows() = %+v, want just the eligible queue", rows)
	}
}
