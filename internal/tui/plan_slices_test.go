package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

var errUnknownAfter = errors.New(`planning payload: slice 1 (csv download) has unknown "after" reference "gone"`)

// planSliceFake records the slice round and creation driven against the session
// dir, so a test can assert nothing is created before confirm and exactly the
// reviewed drafts are created on it.
type planSliceFake struct {
	fakeAppActions
	sliceDir    string
	drafts      []PlanSlice
	createDir   string
	created     []PlanSlice
	createCalls int
	createErr   error
	errChildren []string
}

func (f *planSliceFake) SlicePlan(_ context.Context, dir string) (PlanOutcome, error) {
	f.sliceDir = dir
	return PlanOutcome{Status: "slices", SessionDir: dir, Epic: "COD-800", Slices: f.drafts}, nil
}

func (f *planSliceFake) CreateSlices(_ context.Context, dir string, slices []PlanSlice) (SliceOutcome, error) {
	f.createDir, f.created = dir, slices
	f.createCalls++
	if f.createErr != nil {
		return SliceOutcome{Epic: "COD-800", Children: f.errChildren}, f.createErr
	}
	ids := make([]string, len(slices))
	for i := range slices {
		ids[i] = "COD-90" + string(rune('1'+i))
	}
	return SliceOutcome{Epic: "COD-800", Children: ids, Created: true}, nil
}

func draftSlices() []PlanSlice {
	return []PlanSlice{
		{Title: "export seam", Description: "the seam"},
		{Title: "csv download", After: []string{"export seam"}},
		{Title: "pdf export", After: []string{"export seam"}},
	}
}

// slicesModel returns a Plan screen already showing the slice review list.
func slicesModel(t *testing.T, fake *planSliceFake) planModel {
	t.Helper()
	m := newPlanModel(context.Background(), fake, DefaultStyles(), 100, 40)
	m.step = planRunning
	m.sessionDir = "/plans/session-1"
	m, _ = m.Update(planDoneMsg{out: PlanOutcome{Status: "slices", SessionDir: "/plans/session-1", Epic: "COD-800", Slices: draftSlices()}})
	if m.step != planSlices {
		t.Fatalf("setup: step = %v, want planSlices", m.step)
	}
	return m
}

func sliceKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// TestPlanSlicesRender pins the review list's shape: every draft renders as a
// cursor row with its dependencies, under an epic-scoped header, with the review
// verbs in the footer.
func TestPlanSlicesRender(t *testing.T) {
	m := slicesModel(t, &planSliceFake{})
	body := m.body("·")
	for _, want := range []string{"COD-800", "1. export seam", "2. csv download", "after: export seam"} {
		if !strings.Contains(body, want) {
			t.Errorf("review list missing %q:\n%s", want, body)
		}
	}
	fh := m.help().footer()
	for _, verb := range []string{"edit title", "drop", "reorder", "create children"} {
		if !strings.Contains(fh, verb) {
			t.Errorf("footer missing the %q verb: %q", verb, fh)
		}
	}
}

// TestPlanSlicesEditTitle drives the edit-title verb: e opens the inline editor
// on the cursored row, enter applies the typed title, and "after" references to
// the old title follow the rename.
func TestPlanSlicesEditTitle(t *testing.T) {
	m := slicesModel(t, &planSliceFake{})

	m, _ = m.handleKey(sliceKey('e'))
	if !m.slices.editing {
		t.Fatal("e should open the title editor")
	}
	m.slices.input.SetValue("export scaffolding")
	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.slices.editing {
		t.Fatal("enter should close the editor")
	}
	if got := m.slices.rows[0].slice.Title; got != "export scaffolding" {
		t.Errorf("title = %q, want the edited one", got)
	}
	if got := m.slices.rows[1].slice.After[0]; got != "export scaffolding" {
		t.Errorf("after reference = %q, want it renamed with the title", got)
	}
}

// TestPlanSlicesDropToggle checks x drops the cursored slice and drops references
// to it from the confirmed payload — and that a second x keeps it again.
func TestPlanSlicesDropToggle(t *testing.T) {
	m := slicesModel(t, &planSliceFake{})

	m, _ = m.handleKey(sliceKey('x'))
	if !m.slices.rows[0].dropped {
		t.Fatal("x should drop the cursored slice")
	}
	kept := m.slices.kept()
	if len(kept) != 2 || kept[0].Title != "csv download" {
		t.Fatalf("kept = %+v, want the two undropped slices", kept)
	}
	if len(kept[0].After) != 0 {
		t.Errorf("kept slice still references the dropped one: %v", kept[0].After)
	}

	m, _ = m.handleKey(sliceKey('x'))
	if m.slices.rows[0].dropped {
		t.Error("second x should keep the slice again")
	}
}

// TestPlanSlicesReorder moves the cursored row with J/K, the cursor following it.
func TestPlanSlicesReorder(t *testing.T) {
	m := slicesModel(t, &planSliceFake{})

	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.handleKey(tea.KeyPressMsg{Code: 'j', Text: "J", Mod: tea.ModShift})
	if got := m.slices.rows[2].slice.Title; got != "csv download" {
		t.Errorf("after J rows[2] = %q, want the moved csv download", got)
	}
	if m.slices.cursor != 2 {
		t.Errorf("cursor = %d, want it to follow the row to 2", m.slices.cursor)
	}

	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if got := m.slices.rows[1].slice.Title; got != "csv download" {
		t.Errorf("after shift+up rows[1] = %q, want csv download back", got)
	}
}

// TestPlanSlicesConfirmCreates drives the whole review to confirmation: nothing
// is created while reviewing, c creates exactly the reviewed drafts (edits,
// drops, and order applied) against the session dir, and the confirmation note
// closes the session.
func TestPlanSlicesConfirmCreates(t *testing.T) {
	fake := &planSliceFake{}
	m := slicesModel(t, fake)

	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.handleKey(sliceKey('x')) // drop pdf export
	if fake.createCalls != 0 {
		t.Fatal("reviewing must not create anything")
	}

	m, cmd := m.handleKey(sliceKey('c'))
	if m.step != planCreating {
		t.Fatalf("after c step = %v, want planCreating", m.step)
	}
	if cmd == nil {
		t.Fatal("c should return the CreateSlices cmd")
	}
	next := cmd()
	done, ok := next.(planSlicesDoneMsg)
	if !ok {
		t.Fatalf("CreateSlices cmd produced %T, want planSlicesDoneMsg", next)
	}
	if fake.createCalls != 1 || fake.createDir != "/plans/session-1" {
		t.Errorf("CreateSlices calls=%d dir=%q, want one call on the session dir", fake.createCalls, fake.createDir)
	}
	if len(fake.created) != 2 || fake.created[0].Title != "export seam" || fake.created[1].Title != "csv download" {
		t.Errorf("created drafts = %+v, want the two kept slices in order", fake.created)
	}

	m, _ = m.Update(done)
	if m.step != planNote {
		t.Fatalf("after creation step = %v, want planNote", m.step)
	}
	if !strings.Contains(m.note, "sliced") || !strings.Contains(m.note, "COD-800") {
		t.Errorf("note = %q, want a sliced confirmation naming the epic", m.note)
	}
}

// TestPlanSlicesCancelCreatesNothing backs out of the review with esc: nothing is
// created and the screen returns to the session list, the session still resumable.
func TestPlanSlicesCancelCreatesNothing(t *testing.T) {
	fake := &planSliceFake{}
	m := slicesModel(t, fake)

	m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.step != planList {
		t.Fatalf("after esc step = %v, want planList", m.step)
	}
	if fake.createCalls != 0 {
		t.Error("cancel must not create anything")
	}
}

// TestPlanSlicesConfirmAllDroppedRefuses keeps the review open with an inline
// error instead of creating an empty set.
func TestPlanSlicesConfirmAllDroppedRefuses(t *testing.T) {
	fake := &planSliceFake{}
	m := slicesModel(t, fake)

	for range m.slices.rows {
		m, _ = m.handleKey(sliceKey('x'))
		m, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m, _ = m.handleKey(sliceKey('c'))
	if m.step != planSlices {
		t.Fatalf("step = %v, want to stay on planSlices", m.step)
	}
	if m.slices.err == "" {
		t.Error("confirming an all-dropped review should flag an inline error")
	}
	if fake.createCalls != 0 {
		t.Error("nothing should be created")
	}
}

// TestPlanSlicesCreateErrorStaysInline keeps the drafts and shows the failure in
// the list when creation failed before anything was made, so the review can be
// fixed instead of lost.
func TestPlanSlicesCreateErrorStaysInline(t *testing.T) {
	fake := &planSliceFake{createErr: errUnknownAfter}
	m := slicesModel(t, fake)

	m, cmd := m.handleKey(sliceKey('c'))
	m, _ = m.Update(cmd())
	if m.step != planSlices {
		t.Fatalf("step = %v, want to stay on planSlices", m.step)
	}
	if !strings.Contains(m.slices.err, "unknown") {
		t.Errorf("inline error = %q, want the creation failure", m.slices.err)
	}
	if len(m.slices.rows) != 3 {
		t.Error("drafts should survive a failed creation")
	}
}

// TestPlanSlicesPartialFailureStaysInline fails the confirm after two children
// were already created: the review stays open with its drafts, the error shows
// inline, and the created identifiers are named — and keep being named through
// further edits — so re-confirming duplicates them knowingly.
func TestPlanSlicesPartialFailureStaysInline(t *testing.T) {
	fake := &planSliceFake{
		createErr:   errors.New(`create slice 2 (pdf export): label "ready" not found`),
		errChildren: []string{"COD-901", "COD-902"},
	}
	m := slicesModel(t, fake)

	m, cmd := m.handleKey(sliceKey('c'))
	m, _ = m.Update(cmd())
	if m.step != planSlices {
		t.Fatalf("step = %v, want to stay on planSlices", m.step)
	}
	if !strings.Contains(m.slices.err, "pdf export") {
		t.Errorf("inline error = %q, want the creation failure", m.slices.err)
	}
	if len(m.slices.rows) != 3 {
		t.Error("drafts should survive a partial creation failure")
	}
	body := m.body("·")
	for _, want := range []string{"COD-901", "COD-902", "duplicate"} {
		if !strings.Contains(body, want) {
			t.Errorf("review body missing %q after a partial failure:\n%s", want, body)
		}
	}

	m, _ = m.handleKey(sliceKey('x'))
	body = m.body("·")
	if !strings.Contains(body, "COD-901") {
		t.Errorf("created children should survive further edits:\n%s", body)
	}
}
