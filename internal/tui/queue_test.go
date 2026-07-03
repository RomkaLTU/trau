package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/state"
)

// keyPress builds a KeyPressMsg for a rail verb key ("x", "o", …) or "esc".
func keyPress(s string) tea.KeyPressMsg {
	if s == "esc" {
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// TestQueueSortsByAttention checks the rail ordering contract: needs-human first
// (quarantined/faulted), then in-flight with the live ticket floated to the top
// of its bucket, then not-yet-started, and finished rows fold out of the active
// set entirely.
func TestQueueSortsByAttention(t *testing.T) {
	rows := []QueueRow{
		{ID: "COD-merged", Phase: state.Merged},
		{ID: "COD-inflight", Phase: state.Verified},
		{ID: "COD-live", Phase: state.Building, Live: true},
		{ID: "COD-ready", Phase: ""},
		{ID: "COD-quarantined", Phase: state.Quarantined},
		{ID: "COD-faulted", Phase: state.HandedOff, FailureReason: "agent crashed"},
		{ID: "COD-reset", Phase: phaseReset},
	}
	active, done := partitionQueue(rows)

	var order []string
	for _, r := range active {
		order = append(order, r.ID)
	}
	want := []string{
		"COD-faulted", "COD-quarantined", // needs-human (sorted by ID within bucket)
		"COD-live",     // in-flight, live floats first
		"COD-inflight", // in-flight, not live
		"COD-ready",    // planned
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("active order = %v, want %v", order, want)
	}

	var doneIDs []string
	for _, r := range done {
		doneIDs = append(doneIDs, r.ID)
	}
	if strings.Join(doneIDs, ",") != "COD-merged,COD-reset" {
		t.Errorf("folded rows = %v, want [COD-merged COD-reset]", doneIDs)
	}
}

// TestFoldedDoneLineCounts checks the folded summary counts merged and reset rows
// accurately, and is empty when nothing has finished.
func TestFoldedDoneLineCounts(t *testing.T) {
	s := DefaultStyles()
	done := []QueueRow{
		{ID: "COD-1", Phase: state.Merged},
		{ID: "COD-2", Phase: state.Merged},
		{ID: "COD-3", Phase: phaseReset},
	}
	line := ansi.Strip(foldedDoneLine(s, done, 60))
	if !strings.Contains(line, "2 merged") {
		t.Errorf("folded line = %q, want it to count 2 merged", line)
	}
	if !strings.Contains(line, "1 reset") {
		t.Errorf("folded line = %q, want it to count 1 reset", line)
	}
	if got := foldedDoneLine(s, nil, 60); got != "" {
		t.Errorf("folded line for no done rows = %q, want empty", got)
	}
}

// TestNeedsHumanRowsNeverFold guards that quarantined and faulted rows always
// stay in the selectable set — they are the whole point of the rail.
func TestNeedsHumanRowsNeverFold(t *testing.T) {
	rows := []QueueRow{
		{ID: "COD-q", Phase: state.Quarantined},
		{ID: "COD-f", Phase: state.Verified, FailureReason: "boom"},
		{ID: "COD-m", Phase: state.Merged},
	}
	active, _ := partitionQueue(rows)
	if len(active) != 2 {
		t.Fatalf("active rows = %d, want 2 (both needs-human rows)", len(active))
	}
	for _, r := range active {
		if r.attention() != attnNeedsHuman {
			t.Errorf("row %s attention = %d, want needs-human", r.ID, r.attention())
		}
	}
}

// TestNeedsHumanReasonAlwaysShown guards the AC that quarantined/faulted rows
// surface their reason — even when the cursor is on a different row.
func TestNeedsHumanReasonAlwaysShown(t *testing.T) {
	s := DefaultStyles()
	rows := []QueueRow{
		{ID: "COD-a", Phase: state.Verified}, // in-flight, no reason
		{ID: "COD-q", Phase: state.Quarantined, FailureReason: "husky pre-push failed"},
	}
	// Sorted, COD-q (needs-human) is first; put the cursor on COD-a (index 1) so
	// the quarantined row is not the selected one.
	out := ansi.Strip(renderQueue(s, "*", rows, 1, 60, 0))
	if !strings.Contains(out, "husky pre-push failed") {
		t.Errorf("quarantined reason must surface unselected:\n%s", out)
	}
}

// TestQueueVerbsPerState checks which recovery verbs apply per row and how the
// live context withholds the tree/loop-mutating ones so mid-run actions can't
// disturb the running ticket.
func TestQueueVerbsPerState(t *testing.T) {
	merged := QueueRow{ID: "COD-m", Phase: state.Merged, PRURL: "u"}
	if _, _, resume, branch, reset := queueVerbs(merged, false); resume || branch || reset {
		t.Error("merged row should offer no resume/checkout/reset")
	}
	if open, _, _, _, _ := queueVerbs(merged, false); !open {
		t.Error("merged row with a PR should still offer open")
	}

	fault := QueueRow{ID: "COD-f", Phase: state.Verified, Branch: "b", FailureReason: "x"}
	open, logs, resume, branch, reset := queueVerbs(fault, false)
	if !logs || resume != true || branch != true || reset != true {
		t.Errorf("recoverable row (not live) verbs = open:%v logs:%v resume:%v branch:%v reset:%v, want resume/branch/reset all true", open, logs, resume, branch, reset)
	}

	// Mid-run: resume and checkout are withheld, reset survives for a non-active row.
	_, _, lresume, lbranch, lreset := queueVerbs(fault, true)
	if lresume || lbranch {
		t.Error("live context must withhold resume/checkout so they can't disturb the running ticket")
	}
	if !lreset {
		t.Error("live context should still allow reset of a non-active checkpoint")
	}

	// The live (active) row itself: reset is withheld too.
	live := QueueRow{ID: "COD-live", Phase: state.Building, Live: true}
	if _, _, _, _, r := queueVerbs(live, true); r {
		t.Error("the active ticket's own row must not offer reset")
	}
}

// TestResetConfirmGuardStatus checks the two-key reset confirm on the Status
// screen: x arms the guard (no reset yet), a second x confirms, and esc cancels.
func TestResetConfirmGuardStatus(t *testing.T) {
	m := appModel{
		statusRows: []QueueRow{{ID: "COD-1", Phase: state.Quarantined, Branch: "b"}},
	}

	// First x arms the confirm without issuing a reset command.
	nm, cmd := m.handleStatusKey(keyPress("x"))
	m = nm.(appModel)
	if m.statusConfirmID != "COD-1" {
		t.Fatalf("statusConfirmID after first x = %q, want COD-1", m.statusConfirmID)
	}
	if cmd != nil {
		t.Error("first x should not issue a reset command")
	}

	// A second x confirms and issues the reset, clearing the guard.
	nm, cmd = m.handleStatusKey(keyPress("x"))
	m = nm.(appModel)
	if m.statusConfirmID != "" {
		t.Error("statusConfirmID should clear once confirmed")
	}
	if cmd == nil {
		t.Error("second x should issue the reset command")
	}

	// esc while armed cancels without resetting.
	m.statusConfirmID = "COD-1"
	nm, cmd = m.handleStatusKey(keyPress("esc"))
	m = nm.(appModel)
	if m.statusConfirmID != "" {
		t.Error("esc should clear the pending reset")
	}
	if cmd != nil {
		t.Error("esc should not issue a reset command")
	}
}
