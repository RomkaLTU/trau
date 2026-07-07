package tui

import (
	"context"
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

// TestSelectionBypassDocumented pins that every screen carrying the mouse toggle
// also documents the modifier-drag bypass, so users can copy without toggling and
// discover the ⌥ variant on iTerm2 / Terminal.app.
func TestSelectionBypassDocumented(t *testing.T) {
	bypass := func(h screenHelp) (helpKey, bool) {
		for _, c := range h.columns {
			for _, k := range c.keys {
				if strings.Contains(k.key, "drag") {
					return k, true
				}
			}
		}
		return helpKey{}, false
	}
	m := initialModel(nil)
	helps := map[string]screenHelp{
		"menu":    menuHelp(),
		"status":  statusHelp(),
		"running": m.runningHelp(),
		"summary": m.summaryHelp(),
	}
	for name, h := range helps {
		k, ok := bypass(h)
		if !ok {
			t.Errorf("%s help must document the shift/option-drag selection bypass", name)
			continue
		}
		if !strings.Contains(k.desc, "⌥") {
			t.Errorf("%s bypass help should note the ⌥ (Option) variant, got desc %q", name, k.desc)
		}
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

// locateSub is locateZone for a sub-model whose View returns a plain string (not
// yet scanned): it runs zone.Scan itself, the way the app shell does per frame.
func locateSub(t *testing.T, render func() string, id string) *zone.ZoneInfo {
	t.Helper()
	zone.SetEnabled(true)
	for i := 0; i < 200; i++ {
		zone.Scan(render())
		if z := zone.Get(id); !z.IsZero() {
			return z
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("zone %q never resolved", id)
	return nil
}

// TestMenuRowClickSelectsThenActivates drives real clicks on a menu row: the first
// selects it, a second on the same row runs its action.
func TestMenuRowClickSelectsThenActivates(t *testing.T) {
	app := helpApp(t) // parked on the main menu at 80×24

	z := locateZone(t, app.View, zoneMenuRow+"1") // "Run once"
	next, _ := app.Update(clickAt(z))
	napp := next.(appModel)
	if napp.cursor != 1 {
		t.Fatalf("clicking menu row 1 must select it, cursor=%d", napp.cursor)
	}

	z2 := locateZone(t, napp.View, zoneMenuRow+"1")
	after, _ := napp.Update(clickAt(z2))
	if got := after.(appModel).view; got != viewRunOnce {
		t.Errorf("clicking the already-selected Run once row must open it, view=%v", got)
	}
}

// TestSettingsRowClickSelectsThenEdits checks a settings key row selects on click
// and opens its editor on a click of the already-selected key.
func TestSettingsRowClickSelectsThenEdits(t *testing.T) {
	items := []ConfigItem{
		{Key: "MODEL", Value: "opus", Layer: "local"},
		{Key: "EFFORT", Value: "high", Layer: "local"},
		{Key: "CI", Value: "1", Layer: "project"},
	}
	m := newSettingsModel(&fakeSettingsActions{items: items}, DefaultStyles(), 80, 24)

	z := locateSub(t, m.View, zoneSetRow+"1")
	m2, _ := m.handleMouseClick(clickAt(z))
	if m2.cursor != 1 {
		t.Fatalf("clicking key row 1 must select it, cursor=%d", m2.cursor)
	}

	z2 := locateSub(t, m2.View, zoneSetRow+"1")
	m3, _ := m2.handleMouseClick(clickAt(z2))
	if m3.step != settingsEdit {
		t.Errorf("clicking the already-selected key must open its editor, step=%v", m3.step)
	}
}

// TestRunLoopClickStartsRun is the F1 regression: clicking the selected sub-issue
// on the Run-loop screen must actually start the run (view → running), like the s
// key does — the click path used to swallow the Done state.
func TestRunLoopClickStartsRun(t *testing.T) {
	ls := newLoopSetupModel(context.Background(), &fakeAppActions{}, DefaultStyles(), MenuInfo{}, 80, 24)
	ls.step = loopList
	ls.subs = []SubIssue{{ID: "COD-7", Title: "do a thing"}}
	app := appModel{
		actions: &fakeAppActions{}, baseCtx: context.Background(),
		styles: DefaultStyles(), width: 80, height: 24,
		view: viewRunLoop, loopSetup: ls,
	}

	z := locateZone(t, app.View, zoneLoopRow+"0")
	next, _ := app.Update(clickAt(z))
	if got := next.(appModel).view; got != viewRunning {
		t.Errorf("clicking the selected sub-issue must start the run, view=%v", got)
	}
}

// TestRunOnceClickStartsRun is the F2 regression: clicking the selected ticket on
// the Run-once screen must start it (view → running), like enter does.
func TestRunOnceClickStartsRun(t *testing.T) {
	ro := newRunOnceModel(context.Background(), &fakeAppActions{}, DefaultStyles(), MenuInfo{}, 80, 24)
	ro.step = runOnceList
	ro.eligible = []ListedTicket{{ID: "COD-5", Title: "ship it"}}
	app := appModel{
		actions: &fakeAppActions{}, baseCtx: context.Background(),
		styles: DefaultStyles(), width: 80, height: 24,
		view: viewRunOnce, runOnce: ro,
	}

	z := locateZone(t, app.View, zoneRunOnceRow+"0")
	next, _ := app.Update(clickAt(z))
	if got := next.(appModel).view; got != viewRunning {
		t.Errorf("clicking the selected ticket must start the run, view=%v", got)
	}
}

// TestWheelDismissesToast is the F3 regression: scrolling the wheel dismisses the
// copy toast, like a keypress or click does.
func TestWheelDismissesToast(t *testing.T) {
	rows := []QueueRow{{ID: "COD-1", Phase: state.Quarantined, FailureReason: "boom"}}
	app := runningApp(120, 30, rows)
	app.styles = DefaultStyles()

	copied, _ := app.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	napp := copied.(appModel)
	if napp.dash.toast == "" {
		t.Fatal("y must set the copy toast")
	}
	scrolled, _ := napp.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	if got := scrolled.(appModel).dash.toast; got != "" {
		t.Errorf("wheel scroll must dismiss the toast, got %q", got)
	}
}

// TestMouseOffOverlaySkipsNarrow is the F4 regression: the indicator no-ops rather
// than overflowing when the terminal is narrower than the tag.
func TestMouseOffOverlaySkipsNarrow(t *testing.T) {
	base := "hi\nthere"
	if got := overlayMouseOff(DefaultStyles(), base, 20, 6); got != base {
		t.Error("overlay must no-op when the terminal is narrower than the indicator")
	}
}

// TestProviderTabClickSwitches checks a provider tab responds to a click by
// switching the active provider.
func TestProviderTabClickSwitches(t *testing.T) {
	acts := &fakeSettingsActions{tunings: []ProviderTuning{
		{Name: "claude", Active: true, Model: ProviderTuningField{Value: "opus", Layer: "project"}},
		{Name: "codex", Model: ProviderTuningField{Value: "gpt", Layer: "project"}},
	}}
	m := newProviderSettingsModel(acts, DefaultStyles(), 80, 24)

	z := locateSub(t, m.View, zoneProvTab+"1")
	m2, _ := m.handleMouseClick(clickAt(z))
	if m2.tab != 1 {
		t.Errorf("clicking provider tab 1 must switch to it, tab=%d", m2.tab)
	}
}

// TestFooterVerbClickFiresKey drives real clicks on the q-stop footer verb of the
// running dashboard: the first arms the two-key stop confirm, the second confirms
// it — the same compose the keyboard gets, since a click synthesizes a q keypress.
func TestFooterVerbClickFiresKey(t *testing.T) {
	app := runningApp(120, 30, []QueueRow{{ID: "COD-1", Phase: state.Quarantined, FailureReason: "boom"}})
	app.styles = DefaultStyles()

	// The q verb moves between the normal footer (far right) and the armed confirm
	// (inline); clear any stale coords so each locate resolves the current frame.
	zone.Clear(zoneFooterVerb + "q")
	z := locateZone(t, app.View, zoneFooterVerb+"q")
	armed, _ := app.Update(clickAt(z))
	app = armed.(appModel)
	if app.dash.stopping {
		t.Error("one q-stop click must only arm the confirm, not stop the loop")
	}
	if !app.dash.stopArmed() {
		t.Error("clicking the q-stop footer verb must arm the stop confirm")
	}

	// The armed footer re-marks q on its "q again to confirm" segment, at a new
	// position; drop the stale coords so the second locate resolves it fresh.
	zone.Clear(zoneFooterVerb + "q")
	z = locateZone(t, app.View, zoneFooterVerb+"q")
	confirmed, _ := app.Update(clickAt(z))
	if !confirmed.(appModel).dash.stopping {
		t.Error("a second q-stop click must confirm the stop, like pressing q twice")
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

// TestClicksAbsorbedWhilePeeking checks the peek overlay owns input: a click on a
// rail row still painted past the overlay is absorbed, not fired underneath.
func TestClicksAbsorbedWhilePeeking(t *testing.T) {
	rows := []QueueRow{
		{ID: "COD-1", Phase: state.Quarantined, FailureReason: "boom"},
		{ID: "COD-2", Phase: state.Quarantined, FailureReason: "bang"},
	}
	app := runningApp(120, 30, rows)
	app.styles = DefaultStyles()

	z := locateZone(t, app.View, zoneRailRow+"COD-2")
	peeked, _ := app.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	papp := peeked.(appModel)
	if !papp.dash.peek {
		t.Fatal("space must open the peek preview")
	}

	after, _ := papp.Update(clickAt(z))
	aapp := after.(appModel)
	if aapp.dash.queueCursor != 0 {
		t.Errorf("a click while peeking must be absorbed, cursor moved to %d", aapp.dash.queueCursor)
	}
	if !aapp.dash.peek {
		t.Error("peek must stay open after an absorbed click")
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
