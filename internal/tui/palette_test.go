package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	keyCtrlP = tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	keyColon = tea.KeyPressMsg{Code: ':', Text: ":"}
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
)

// typePalette feeds each rune of s to the model as a key press, threading the
// updated model through.
func typePalette(t *testing.T, m appModel, s string) appModel {
	t.Helper()
	for _, r := range s {
		nm, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = nm.(appModel)
	}
	return m
}

// TestPaletteOpensOnEveryView drives ctrl+p open / esc close on each screen and
// asserts the palette composites without changing the 80×24 frame (AC 1).
func TestPaletteOpensOnEveryView(t *testing.T) {
	sel := func(a menuAction) func(appModel) appModel {
		return func(m appModel) appModel { am, _ := m.selectAction(a); return am.(appModel) }
	}
	screens := []struct {
		name  string
		build func(appModel) appModel
	}{
		{"menu", func(m appModel) appModel { return m }},
		{"more", sel(actMore)},
		{"status", sel(actStatus)},
		{"logs", sel(actLogs)},
		{"version", sel(actVersion)},
		{"reset", sel(actReset)},
		{"run loop", sel(actRun)},
		{"run once", sel(actRunOnce)},
		{"settings", sel(actSettings)},
		{"onboarding", sel(actOnboarding)},
		{"running", func(m appModel) appModel {
			rm, _ := m.startRunLoop("")
			r := rm.(appModel)
			if r.loopCancel != nil {
				r.loopCancel()
			}
			return r
		}},
	}
	for _, sc := range screens {
		t.Run(sc.name, func(t *testing.T) {
			m := sc.build(helpApp(t))

			nm, _ := m.Update(keyCtrlP)
			m = nm.(appModel)
			if !m.palette.active {
				t.Fatalf("ctrl+p did not open the palette on %s", sc.name)
			}
			over := m.render()
			if h := lipgloss.Height(over); h != 24 {
				t.Errorf("%s palette height = %d, want 24", sc.name, h)
			}
			if w := lipgloss.Width(over); w > 80 {
				t.Errorf("%s palette width = %d, overflows 80", sc.name, w)
			}
			if !strings.Contains(ansi.Strip(over), "Command palette") {
				t.Errorf("%s did not render the palette panel", sc.name)
			}

			nm, _ = m.Update(keyEscape)
			if nm.(appModel).palette.active {
				t.Errorf("esc did not close the palette on %s", sc.name)
			}
		})
	}
}

// TestPaletteColonGatedByEditing confirms : opens the palette outside text entry
// but is typed as a literal while a free-text field is focused, and ctrl+p opens
// even there (AC 1).
func TestPaletteColonGatedByEditing(t *testing.T) {
	m := helpApp(t)
	nm, _ := m.Update(keyColon)
	if !nm.(appModel).palette.active {
		t.Fatal(": did not open the palette on the menu")
	}

	am, _ := helpApp(t).selectAction(actOnboarding)
	edit := am.(appModel)
	// Drive the onboarding form to a focused text field (jira base URL).
	edit.onboard.fv.tracker = "jira"
	edit.onboard.phase = phaseForm
	edit.onboard.form = edit.onboard.newForm()
	edit.onboard = driveCmds(edit.onboard, edit.onboard.form.Init())
	edit.onboard = pressKey(edit.onboard, tea.KeyEnter)
	edit.onboard = pressKey(edit.onboard, tea.KeyEnter)
	if !edit.editing() {
		t.Fatal("precondition: jira-creds step should be a text-entry context")
	}
	nm, _ = edit.Update(keyColon)
	if nm.(appModel).palette.active {
		t.Error(": opened the palette while a free-text field was focused")
	}
	nm, _ = edit.Update(keyCtrlP)
	if !nm.(appModel).palette.active {
		t.Error("ctrl+p should open the palette even during text entry")
	}
}

// TestPaletteGlobalActionsFromRegistry proves every real menu/More action is a
// palette command (no drift) and the structural entries are not (AC 2 & 4).
func TestPaletteGlobalActionsFromRegistry(t *testing.T) {
	m := helpApp(t)
	cmds := m.paletteCommands()
	have := map[string]bool{}
	for _, c := range cmds {
		have[c.title] = true
	}

	for _, it := range append(append([]menuItem{}, m.items...), m.moreItems...) {
		switch it.action {
		case actMore, actBack:
			if have[it.title] {
				t.Errorf("structural action %q should not be a palette command", it.title)
			}
			continue
		}
		if !have[it.title] {
			t.Errorf("menu action %q is missing from the palette registry", it.title)
		}
	}
	if !have["Quit"] {
		t.Error("Quit should be reachable from the palette")
	}
}

// TestPaletteFuzzyDispatch confirms a fuzzy match lands on the same behavior as
// the action's menu path (AC 2).
func TestPaletteFuzzyDispatch(t *testing.T) {
	m := helpApp(t)

	var run *paletteCommand
	for _, c := range m.paletteMatches("run once") {
		if c.title == "Run once" {
			c := c
			run = &c
			break
		}
	}
	if run == nil {
		t.Fatal(`"run once" did not fuzzy-match the Run once command`)
	}
	nm, _ := run.run(m)
	if got := nm.(appModel).view; got != viewRunOnce {
		t.Errorf("palette Run once landed on view %d, want viewRunOnce (%d)", got, viewRunOnce)
	}
}

// TestPaletteTicketVerbsStateGated proves typing a ticket ID surfaces only the
// verbs its local state allows, and executing them works (AC 3).
func TestPaletteTicketVerbsStateGated(t *testing.T) {
	m := helpApp(t)

	// The fake exposes COD-1 with both a checkpoint and saved logs.
	titles := func(cmds []paletteCommand) map[string]bool {
		out := map[string]bool{}
		for _, c := range cmds {
			out[c.title] = true
		}
		return out
	}

	known := titles(m.paletteMatches("COD-1"))
	for _, want := range []string{"Resume COD-1", "Logs COD-1", "Open COD-1", "Reset COD-1"} {
		if !known[want] {
			t.Errorf("known ticket COD-1 missing verb %q", want)
		}
	}
	if known["Run COD-1"] {
		t.Error("a resumable ticket should offer Resume, not Run")
	}

	// A ticket with no local state offers only run + open.
	fresh := titles(m.paletteMatches("COD-999"))
	for _, want := range []string{"Run COD-999", "Open COD-999"} {
		if !fresh[want] {
			t.Errorf("fresh ticket COD-999 missing verb %q", want)
		}
	}
	for _, absent := range []string{"Resume COD-999", "Logs COD-999", "Reset COD-999"} {
		if fresh[absent] {
			t.Errorf("fresh ticket COD-999 should not offer %q", absent)
		}
	}
}

// TestPaletteTicketDispatch runs a ticket verb end to end through the key path:
// COD-999 has no global fuzzy matches, so its verbs sit alone and the cursor is
// deterministic (AC 3).
func TestPaletteTicketDispatch(t *testing.T) {
	// Run a fresh ticket → the running dashboard.
	m := helpApp(t)
	nm, _ := m.Update(keyCtrlP)
	m = typePalette(t, nm.(appModel), "COD-999")
	nm, _ = m.Update(keyEnter)
	m = nm.(appModel)
	if m.view != viewRunning {
		t.Fatalf("enter on Run COD-999 landed on view %d, want viewRunning (%d)", m.view, viewRunning)
	}
	if m.palette.active {
		t.Error("running a command should close the palette")
	}
	if m.loopCancel != nil {
		m.loopCancel()
	}

	// Reset a known ticket → the pre-filled reset screen (confirm step).
	m = helpApp(t)
	nm, _ = m.Update(keyCtrlP)
	m = typePalette(t, nm.(appModel), "COD-1")
	// verbs: Resume(0) Logs(1) Open(2) Reset(3)
	for i := 0; i < 3; i++ {
		nm, _ = m.Update(keyDown)
		m = nm.(appModel)
	}
	nm, _ = m.Update(keyEnter)
	m = nm.(appModel)
	if m.view != viewReset {
		t.Fatalf("enter on Reset COD-1 landed on view %d, want viewReset (%d)", m.view, viewReset)
	}
	if got := m.reset.Value(); got != "COD-1" {
		t.Errorf("reset field = %q, want COD-1 pre-filled for confirm", got)
	}
}

// TestPaletteCursorClampsToMatches proves the cursor never points past the
// filtered list as it narrows (AC 3 dispatch safety).
func TestPaletteCursorClampsToMatches(t *testing.T) {
	m := helpApp(t)
	nm, _ := m.Update(keyCtrlP)
	m = nm.(appModel)

	// Move the cursor down the full unfiltered list, then filter to one verb.
	for i := 0; i < 8; i++ {
		nm, _ = m.Update(keyDown)
		m = nm.(appModel)
	}
	m = typePalette(t, m, "COD-999")
	if n := len(m.paletteMatches(m.palette.filter)); m.palette.cursor >= n {
		t.Errorf("cursor %d out of range for %d matches", m.palette.cursor, n)
	}
	// Typing resets the cursor to the top, so enter runs the first verb.
	if m.palette.cursor != 0 {
		t.Errorf("cursor after filtering = %d, want 0", m.palette.cursor)
	}
}
