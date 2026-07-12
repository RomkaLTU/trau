package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// keyEnter is shared with palette_test.go's package-level key fixtures.
var (
	keySpace = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEsc}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

// TestPeekContentByRowState checks the peek preview selects state-appropriate
// content per row: the live tail for the active ticket, the failure reason for a
// quarantined one, the PR summary for a merged one, and a short status otherwise.
func TestPeekContentByRowState(t *testing.T) {
	// Live: a running ticket whose active-phase tail is surfaced.
	live := initialModel(nil)
	live.startTicket("COD-1")
	live.steps = startPhase(live.steps, "build", time.Now())
	live.addLog("· hello-from-agent")

	liveTitle, liveBody := live.peekContent(QueueRow{ID: "COD-1", Live: true}, 60, 10)
	if !strings.Contains(liveTitle, "COD-1") {
		t.Errorf("live title = %q, want the ticket id", liveTitle)
	}
	if body := strip(strings.Join(liveBody, "\n")); !strings.Contains(body, "hello-from-agent") {
		t.Errorf("live peek must surface the active tail, got:\n%s", body)
	}

	m := initialModel(nil)
	cases := []struct {
		name     string
		row      QueueRow
		wantHead string
		wantBody []string
	}{
		{
			name:     "quarantined",
			row:      QueueRow{ID: "COD-9", Phase: state.Quarantined, FailureReason: "husky pre-push rejected: PHPStan boom", Branch: "fix/x"},
			wantHead: "quarantined",
			wantBody: []string{"PHPStan boom", "fix/x"},
		},
		{
			name:     "faulted (no quarantine phase, has reason)",
			row:      QueueRow{ID: "COD-8", Phase: state.HandedOff, FailureReason: "agent crashed mid-handoff"},
			wantHead: "COD-8",
			wantBody: []string{"agent crashed mid-handoff"},
		},
		{
			name:     "merged",
			row:      QueueRow{ID: "COD-3", Phase: state.Merged, PRURL: "https://gh/pr/3", Tokens: 1200, Cost: 1.5, CostMetered: true},
			wantHead: "merged",
			wantBody: []string{"merged", "gh/pr/3"},
		},
		{
			name:     "reset",
			row:      QueueRow{ID: "COD-4", Phase: phaseReset},
			wantHead: "reset",
			wantBody: []string{"restored"},
		},
		{
			name:     "ready",
			row:      QueueRow{ID: "COD-5", Phase: ""},
			wantHead: "ready",
			wantBody: []string{"Queued"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, body := m.peekContent(tc.row, 60, 10)
			if !strings.Contains(title, tc.wantHead) {
				t.Errorf("title = %q, want it to contain %q", title, tc.wantHead)
			}
			joined := strip(strings.Join(body, "\n"))
			for _, want := range tc.wantBody {
				if !strings.Contains(joined, want) {
					t.Errorf("body missing %q, got:\n%s", want, joined)
				}
			}
		})
	}
}

// TestSpaceOpensPeekEscCloses checks space floats the preview over the running
// dashboard and esc closes it, with the loop untouched underneath.
func TestSpaceOpensPeekEscCloses(t *testing.T) {
	m := runningApp(120, 32, []QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "boom"}})

	nm, _ := m.handleRunningKey(keySpace)
	m = nm.(appModel)
	if !m.dash.peeking() {
		t.Fatal("space must open the peek preview")
	}
	if out := strip(m.render()); !strings.Contains(out, "esc close") {
		t.Errorf("peek overlay must render its nav, got:\n%s", out)
	}

	nm, _ = m.handleRunningKey(keyEsc)
	m = nm.(appModel)
	if m.dash.peeking() {
		t.Error("esc must close the peek preview")
	}
}

// TestEnterAttachesLiveElsePeeks checks enter attaches the live agent view when
// the active ticket is selected, and peeks any other row.
func TestEnterAttachesLiveElsePeeks(t *testing.T) {
	liveDash := freshDash(120, 32, "").withQueue([]QueueRow{{ID: "COD-1", Phase: state.Building}})
	liveDash.startTicket("COD-1")
	liveDash.streamID = "1-build"
	if sel, _ := liveDash.selectedRow(); !sel.Live {
		t.Fatalf("expected the live ticket selected, got %+v", sel)
	}
	nm, _, handled := liveDash.handleKey(keyEnter)
	if !handled || !nm.streaming {
		t.Fatalf("enter on the live row must attach (handled=%v streaming=%v)", handled, nm.streaming)
	}

	otherDash := freshDash(120, 32, "").withQueue([]QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "boom"}})
	nm, _, handled = otherDash.handleKey(keyEnter)
	if !handled || nm.streaming {
		t.Fatalf("enter on a non-live row must not attach (streaming=%v)", nm.streaming)
	}
	if !nm.peeking() {
		t.Error("enter on a non-live row must peek it")
	}
}

// TestPeekIsModalQDoesNotStop checks the preview is modal: q closes it instead of
// stopping the loop, while ctrl+c is still the emergency stop.
func TestPeekIsModalQDoesNotStop(t *testing.T) {
	stopped := false
	m := runningApp(120, 32, []QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "boom"}})
	m.loopCancel = func() { stopped = true }

	nm, _ := m.handleRunningKey(keySpace)
	m = nm.(appModel)
	nm, _ = m.handleRunningKey(keyPress("q"))
	m = nm.(appModel)
	if stopped {
		t.Error("q while peeking must not stop the loop")
	}
	if m.dash.peeking() {
		t.Error("q while peeking must close the preview")
	}
	if m.dash.stopping {
		t.Error("q while peeking must not arm the stop")
	}

	nm, _ = m.handleRunningKey(keySpace)
	m = nm.(appModel)
	nm, _ = m.handleRunningKey(keyCtrlC)
	m = nm.(appModel)
	if !stopped {
		t.Error("ctrl+c must always stop the loop, even while peeking")
	}
}

// TestAttachReframesPaneTitle checks the attached live view names itself and its
// way out in the pane border.
func TestAttachReframesPaneTitle(t *testing.T) {
	d := freshDash(120, 32, "")
	d.streaming = true
	d.streamID = "1-build"
	out := strip(d.render())
	if !strings.Contains(out, "Attached") || !strings.Contains(out, "esc detach") {
		t.Errorf("attached pane must show the attached state and detach hint, got:\n%s", out)
	}
}

// TestPeekDoesNotSwallowCtrlC checks the standalone dashboard renderer still stops
// on ctrl+c while peeking — the emergency stop must not be trapped by the modal.
func TestPeekDoesNotSwallowCtrlC(t *testing.T) {
	interrupted := false
	d := freshDash(120, 32, "").withQueue([]QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "boom"}})
	d.onInterrupt = func() { interrupted = true }

	d, _, _ = d.handleKey(keySpace)
	if !d.peek {
		t.Fatal("space must open the peek preview")
	}
	d, _, handled := d.handleKey(keyCtrlC)
	if !handled || !interrupted || !d.stopping {
		t.Fatalf("ctrl+c while peeking must stop the loop (handled=%v interrupted=%v stopping=%v)", handled, interrupted, d.stopping)
	}
}

// TestPeekSelfHealsWhenUnpeekable checks the modal auto-closes when its row can no
// longer be previewed (here: the terminal shrinks below the rail), so it can never
// swallow keys behind an invisible card.
func TestPeekSelfHealsWhenUnpeekable(t *testing.T) {
	d := freshDash(120, 32, "").withQueue([]QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "boom"}})
	d, _, _ = d.handleKey(keySpace)
	if !d.peek {
		t.Fatal("space must open the peek preview")
	}
	nm, _ := d.Update(tea.WindowSizeMsg{Width: 70, Height: 32})
	if nm.(model).peek {
		t.Error("peek must self-heal closed when the rail is no longer drawn")
	}
}

// TestPeekMergedBeatsStaleFailure checks a merged ticket carrying a stale
// FailureReason previews the merged summary, not a failure card.
func TestPeekMergedBeatsStaleFailure(t *testing.T) {
	m := initialModel(nil)
	_, body := m.peekContent(QueueRow{ID: "COD-7", Phase: state.Merged, FailureReason: "old transient boom", PRURL: "https://gh/pr/7"}, 60, 10)
	joined := strip(strings.Join(body, "\n"))
	if !strings.Contains(joined, "merged") {
		t.Errorf("merged row must show the merged summary, got:\n%s", joined)
	}
	if strings.Contains(joined, "old transient boom") {
		t.Errorf("merged row must not surface the stale failure reason, got:\n%s", joined)
	}
}

// TestPaletteDismissesPeek checks opening the command palette (ctrl+p, which
// bypasses editing()) closes the peek preview instead of stacking over it.
func TestPaletteDismissesPeek(t *testing.T) {
	m := runningApp(120, 32, []QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "boom"}})
	nm, _ := m.handleRunningKey(keySpace)
	m = nm.(appModel)
	if !m.dash.peeking() {
		t.Fatal("space must open the peek preview")
	}
	nm2, _ := m.handleKey(keyCtrlP)
	m = nm2.(appModel)
	if !m.palette.active {
		t.Error("ctrl+p must open the palette")
	}
	if m.dash.peeking() {
		t.Error("opening the palette must dismiss the peek preview, not stack over it")
	}
}

// runningApp builds an app shell parked on the live dashboard with a seeded queue.
func runningApp(w, h int, rows []QueueRow) appModel {
	return appModel{
		actions: &fakeAppActions{},
		baseCtx: context.Background(),
		view:    viewRunning,
		width:   w,
		height:  h,
		dash:    freshDash(w, h, "").withQueue(rows),
	}
}
