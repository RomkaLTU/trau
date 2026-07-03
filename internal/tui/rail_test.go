package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestDimsSplitByWidth checks the running body splits into span pane + rail on a
// wide terminal and collapses to a full-width span pane on a narrow one.
func TestDimsSplitByWidth(t *testing.T) {
	wide := freshDash(120, 32, "").dims()
	if wide.railW == 0 {
		t.Error("expected a queue rail at 120 cols")
	}
	if wide.spanW+wide.railW+1 != wide.bodyW {
		t.Errorf("span(%d)+rail(%d)+gap != body(%d)", wide.spanW, wide.railW, wide.bodyW)
	}

	narrow := freshDash(70, 32, "").dims()
	if narrow.railW != 0 {
		t.Errorf("expected no rail at 70 cols, got railW=%d", narrow.railW)
	}
	if narrow.spanW != narrow.bodyW {
		t.Errorf("narrow span pane should be full width: span=%d body=%d", narrow.spanW, narrow.bodyW)
	}
}

// TestLiveOverlayMarksActiveTicket checks the running snapshot overlays the
// active ticket: its row is Live, shows its precise active phase, and drops any
// stale failure reason; and the active ticket is injected when the store has no
// row for it yet.
func TestLiveOverlayMarksActiveTicket(t *testing.T) {
	d := freshDash(120, 32, "").withQueue([]QueueRow{
		{ID: "COD-1", Phase: state.Verified, FailureReason: "stale"},
	})
	d.startTicket("COD-1")
	d.steps = startPhase(d.steps, "verify", time.Now())

	rows := d.liveQueueRows()
	var got *QueueRow
	for i := range rows {
		if rows[i].ID == "COD-1" {
			got = &rows[i]
		}
	}
	if got == nil || !got.Live {
		t.Fatalf("active ticket row = %+v, want Live", got)
	}
	if got.FailureReason != "" {
		t.Error("active ticket must not carry a stale failure reason")
	}
	if got.Desc == "" {
		t.Error("active ticket should show its live phase")
	}

	// Injection: no store row for the active ticket.
	d2 := freshDash(120, 32, "")
	d2.startTicket("COD-2")
	rows2 := d2.liveQueueRows()
	if len(rows2) != 1 || rows2[0].ID != "COD-2" || !rows2[0].Live {
		t.Errorf("expected an injected live COD-2 row, got %+v", rows2)
	}
}

// TestRunStartSeedsRail checks starting a run seeds the rail from the store and
// renders the queue pane on a wide terminal.
func TestRunStartSeedsRail(t *testing.T) {
	base := newAppModel(context.Background(), &fakeAppActions{}, nil)
	nm, _ := base.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	rm, _ := nm.(appModel).startRunLoop("")
	m := rm.(appModel)
	defer m.loopCancel()

	if len(m.dash.queue) == 0 {
		t.Fatal("run start did not seed the rail from the store")
	}
	if !strings.Contains(m.render(), "Queue") {
		t.Error("running view did not render the queue rail")
	}
}

// TestRunningRailCursorAndReset checks live key routing: ↓ moves the rail
// selection, and x on a quarantined non-active row arms the two-key reset confirm
// then fires the reset on the second x.
func TestRunningRailCursorAndReset(t *testing.T) {
	m := appModel{
		actions: &fakeAppActions{},
		baseCtx: context.Background(),
		view:    viewRunning,
		width:   120,
		height:  32,
	}
	m.dash = freshDash(120, 32, "").withQueue([]QueueRow{
		{ID: "COD-q", Phase: state.Quarantined, Branch: "b"},
		{ID: "COD-v", Phase: state.Verified},
	})

	// ↓ moves selection off the quarantined row onto the in-flight one.
	nm, _ := m.handleRunningKey(keyPress("j"))
	m = nm.(appModel)
	if sel, _ := m.dash.selectedRow(); sel.ID != "COD-v" {
		t.Fatalf("after ↓ selected = %q, want COD-v", sel.ID)
	}

	// Back up to the quarantined row and arm the reset.
	nm, _ = m.handleRunningKey(keyPress("k"))
	m = nm.(appModel)
	nm, cmd := m.handleRunningKey(keyPress("x"))
	m = nm.(appModel)
	if m.dash.pendingResetID() != "COD-q" {
		t.Fatalf("pending reset = %q, want COD-q", m.dash.pendingResetID())
	}
	if cmd != nil {
		t.Error("first x should only arm the confirm, not reset")
	}

	// Second x confirms.
	nm, cmd = m.handleRunningKey(keyPress("x"))
	m = nm.(appModel)
	if m.dash.pendingResetID() != "" {
		t.Error("reset confirm should clear after the second x")
	}
	if cmd == nil {
		t.Error("second x should issue the reset command")
	}
}
