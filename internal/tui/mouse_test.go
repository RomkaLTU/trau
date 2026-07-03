package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestMain initializes the global bubblezone manager once for the package, the way
// New/RunSession do in production, so any View() that runs zone.Scan has a manager.
func TestMain(m *testing.M) {
	zone.NewGlobal()
	os.Exit(m.Run())
}

var keyCtrlT = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}

// TestCopyArtifactByRowState pins the y-copy precedence: a merged row yields its
// PR (even with a stale failure reason), a faulted/quarantined row its reason
// (even with a PR), and everything else the ticket id.
func TestCopyArtifactByRowState(t *testing.T) {
	cases := []struct {
		name      string
		row       QueueRow
		wantText  string
		wantLabel string
	}{
		{"merged→PR", QueueRow{ID: "COD-3", Phase: state.Merged, PRURL: "https://gh/pr/3"}, "https://gh/pr/3", "PR URL"},
		{"merged with stale reason still PR", QueueRow{ID: "COD-3", Phase: state.Merged, PRURL: "https://gh/pr/3", FailureReason: "old transient blip"}, "https://gh/pr/3", "PR URL"},
		{"quarantined→reason over PR", QueueRow{ID: "COD-9", Phase: state.Quarantined, FailureReason: "PHPStan boom", PRURL: "https://gh/pr/9"}, "PHPStan boom", "failure reason"},
		{"faulted→reason", QueueRow{ID: "COD-8", Phase: state.HandedOff, FailureReason: "agent crashed mid-handoff"}, "agent crashed mid-handoff", "failure reason"},
		{"ready→id", QueueRow{ID: "COD-1"}, "COD-1", "ticket ID"},
		{"reset→id", QueueRow{ID: "COD-2", Phase: phaseReset}, "COD-2", "ticket ID"},
	}
	for _, c := range cases {
		text, label := copyArtifact(c.row)
		if text != c.wantText || label != c.wantLabel {
			t.Errorf("%s: copyArtifact = (%q, %q), want (%q, %q)", c.name, text, label, c.wantText, c.wantLabel)
		}
	}
}

// TestMouseToggleFlipsReporting checks ctrl+t flips the per-frame mouse mode both
// in the app shell and the standalone dashboard, so the terminal reclaims native
// drag-to-select while off.
func TestMouseToggleFlipsReporting(t *testing.T) {
	// Toggling drives the global zone-enabled flag; leave it on for other tests.
	t.Cleanup(func() { zone.SetEnabled(true) })
	// Standalone dashboard.
	m := initialModel(nil)
	m.width, m.height = 80, 24
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("mouse should start enabled, got %v", got)
	}
	off, _, handled := m.handleKey(keyCtrlT)
	if !handled || !off.mouseOff {
		t.Fatalf("ctrl+t must toggle mouse off (handled=%v off=%v)", handled, off.mouseOff)
	}
	if got := off.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("mouse-off frame must disable reporting, got %v", got)
	}
	on, _, _ := off.handleKey(keyCtrlT)
	if on.mouseOff {
		t.Error("ctrl+t must toggle mouse back on")
	}

	// App shell: ctrl+t is global, works from a card screen too.
	app := runningApp(80, 24, nil)
	app.styles = DefaultStyles()
	next, _ := app.Update(keyCtrlT)
	napp := next.(appModel)
	if !napp.mouseOff {
		t.Fatalf("ctrl+t must toggle the app-shell mouse off")
	}
	if got := napp.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("app-shell mouse-off frame must disable reporting, got %v", got)
	}
}

// TestMouseOffOverlayIndicator checks the mouse-off frame carries a visible footer
// indicator naming the re-enable key.
func TestMouseOffOverlayIndicator(t *testing.T) {
	out := strip(overlayMouseOff(DefaultStyles(), "line one\nline two", 48, 6))
	if !strings.Contains(out, "mouse off") {
		t.Errorf("overlay must show the mouse-off indicator, got:\n%s", out)
	}
	if !strings.Contains(out, "ctrl+t") {
		t.Errorf("overlay must name the re-enable key, got:\n%s", out)
	}
}

// TestCopyKeyYanksSelectedArtifact drives y on the live rail: it sets the copy
// toast, returns an OSC52 command, and the next key dismisses the toast.
func TestCopyKeyYanksSelectedArtifact(t *testing.T) {
	keyY := tea.KeyPressMsg{Code: 'y', Text: "y"}
	rows := []QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "PHPStan boom"}}
	app := runningApp(80, 24, rows)

	next, cmd := app.Update(keyY)
	napp := next.(appModel)
	if !strings.Contains(napp.dash.toast, "failure reason") {
		t.Fatalf("y must set a copy toast naming the artifact, got %q", napp.dash.toast)
	}
	if cmd == nil {
		t.Fatal("y must return an OSC52 SetClipboard command")
	}

	after, _ := napp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := after.(appModel).dash.toast; got != "" {
		t.Errorf("the next key must dismiss the toast, got %q", got)
	}
}

// TestVerbKey pins the footer label → canonical key extraction that drives both
// the click zone id and the synthesized key.
func TestVerbKey(t *testing.T) {
	cases := []struct {
		part   string
		want   string
		wantOK bool
	}{
		{"o open", "o", true},
		{"R reconcile", "R", true},
		{"/ filter", "/", true},
		{"space peek", "space", true},
		{"enter attach", "enter", true},
		{"esc/q back", "esc", true},
		{"enter/e edit", "enter", true},
		{"⇥ switch provider", "tab", true},
		{"y copy PR URL", "y", true},
		{"↑↓ move", "", false},
		{"←→ switch provider", "", false},
	}
	for _, c := range cases {
		k, ok := verbKey(c.part)
		if k != c.want || ok != c.wantOK {
			t.Errorf("verbKey(%q) = (%q,%v), want (%q,%v)", c.part, k, ok, c.want, c.wantOK)
		}
	}
}

// TestSynthVerbKey checks a canonical key round-trips to a key press the handlers
// recognize by String().
func TestSynthVerbKey(t *testing.T) {
	for k, want := range map[string]string{"esc": "esc", "enter": "enter", "space": "space", "tab": "tab", "o": "o", "R": "R"} {
		if got := synthVerbKey(k).String(); got != want {
			t.Errorf("synthVerbKey(%q).String() = %q, want %q", k, got, want)
		}
	}
}

// locateZone renders the view until the async bubblezone worker has published the
// zone's coordinates, then returns them. Mirrors bubblezone's own test pattern.
func locateZone(t *testing.T, view func() tea.View, id string) *zone.ZoneInfo {
	t.Helper()
	// The enabled flag is global and other tests toggle it (mouse-off); ensure it's
	// on so Mark actually emits the markers this hit-test depends on.
	zone.SetEnabled(true)
	for i := 0; i < 200; i++ {
		view() // triggers zone.Scan on the rendered frame
		if z := zone.Get(id); !z.IsZero() {
			return z
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("zone %q never resolved", id)
	return nil
}

func clickAt(z *zone.ZoneInfo) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft}
}

// TestFooterVerbClickFiresKey drives a real click on the q-stop footer verb of the
// running dashboard and checks it stops the loop, same as pressing q.
func TestFooterVerbClickFiresKey(t *testing.T) {
	app := runningApp(120, 30, []QueueRow{{ID: "COD-1", Phase: state.Quarantined, FailureReason: "boom"}})
	app.styles = DefaultStyles()

	z := locateZone(t, app.View, zoneFooterVerb+"q")
	next, _ := app.Update(clickAt(z))
	if !next.(appModel).dash.stopping {
		t.Error("clicking the q-stop footer verb must stop the loop, like pressing q")
	}
}

// TestRailRowClickSelectsThenActivates drives real clicks on a rail row: the first
// selects it, a second on the same row opens its peek preview.
func TestRailRowClickSelectsThenActivates(t *testing.T) {
	rows := []QueueRow{
		{ID: "COD-1", Phase: state.Quarantined, FailureReason: "boom"},
		{ID: "COD-2", Phase: state.Quarantined, FailureReason: "bang"},
	}
	app := runningApp(120, 30, rows)
	app.styles = DefaultStyles()

	z := locateZone(t, app.View, zoneRailRow+"COD-2")
	next, _ := app.Update(clickAt(z))
	napp := next.(appModel)
	if napp.dash.queueCursor != 1 {
		t.Fatalf("clicking COD-2 must select it, cursor=%d", napp.dash.queueCursor)
	}
	if napp.dash.peek {
		t.Fatal("the first click selects only, it must not peek")
	}

	z2 := locateZone(t, napp.View, zoneRailRow+"COD-2")
	after, _ := napp.Update(clickAt(z2))
	if !after.(appModel).dash.peek {
		t.Error("clicking the already-selected row must open its peek preview")
	}
}

// TestWheelOverRailMovesSelection checks the wheel scrolls the region under the
// pointer: over the rail it advances the selection rather than the span pane.
func TestWheelOverRailMovesSelection(t *testing.T) {
	rows := []QueueRow{
		{ID: "COD-1", Phase: state.Quarantined, FailureReason: "boom"},
		{ID: "COD-2", Phase: state.Quarantined, FailureReason: "bang"},
	}
	app := runningApp(120, 30, rows)
	app.styles = DefaultStyles()

	z := locateZone(t, app.View, zoneRail)
	wheel := tea.MouseWheelMsg{X: z.StartX + 1, Y: z.StartY + 1, Button: tea.MouseWheelDown}
	next, _ := app.Update(wheel)
	if got := next.(appModel).dash.queueCursor; got != 1 {
		t.Errorf("wheel-down over the rail must advance the selection, cursor=%d", got)
	}
}
