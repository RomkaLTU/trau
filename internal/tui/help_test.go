package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/console"
)

var (
	keyQuestion = tea.KeyPressMsg{Code: '?', Text: "?"}
	keyEscape   = tea.KeyPressMsg{Code: tea.KeyEsc}
)

func helpApp(t *testing.T) appModel {
	t.Helper()
	base := newAppModel(context.Background(), &fakeAppActions{}, nil)
	nm, _ := base.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return nm.(appModel)
}

// TestHelpOverlayLifecycleOnEveryView drives ? open / esc close on each screen
// named in COD-665 and asserts the overlay composites without changing the 80×24
// frame and that closing restores the exact pre-overlay render (AC 1 & 4).
func TestHelpOverlayLifecycleOnEveryView(t *testing.T) {
	sel := func(a menuAction) func(appModel) appModel {
		return func(m appModel) appModel { am, _ := m.selectAction(a); return am.(appModel) }
	}
	screens := []struct {
		name   string
		build  func(appModel) appModel
		stable bool // render is deterministic (no elapsed-time in the frame)
	}{
		{"menu", func(m appModel) appModel { return m }, true},
		{"more", sel(actMore), true},
		{"status", sel(actStatus), true},
		{"logs", sel(actLogs), true},
		{"version", sel(actVersion), true},
		{"reset", sel(actReset), true},
		{"run loop", sel(actRun), true},
		{"run once", sel(actRunOnce), true},
		{"settings", sel(actSettings), true},
		{"onboarding", sel(actOnboarding), true},
		{"running", func(m appModel) appModel {
			rm, _ := m.startRunLoop("")
			r := rm.(appModel)
			if r.loopCancel != nil {
				r.loopCancel()
			}
			return r
		}, false},
	}
	for _, sc := range screens {
		t.Run(sc.name, func(t *testing.T) {
			m := sc.build(helpApp(t))
			if m.editing() {
				t.Skipf("%s starts in text entry; ? is a literal there", sc.name)
			}
			before := m.render()

			nm, _ := m.Update(keyQuestion)
			m = nm.(appModel)
			if !m.help.active {
				t.Fatalf("? did not open help on %s", sc.name)
			}
			over := m.render()
			// The frame is 80×24 internally; Canvas.Render trims trailing blanks
			// per line, so height is exact and width must never *overflow* 80.
			if h := lipgloss.Height(over); h != 24 {
				t.Errorf("%s overlay height = %d, want 24", sc.name, h)
			}
			if w := lipgloss.Width(over); w > 80 {
				t.Errorf("%s overlay width = %d, overflows 80", sc.name, w)
			}
			if !strings.Contains(ansi.Strip(over), "Help") {
				t.Errorf("%s overlay did not render the help panel", sc.name)
			}
			if over == before {
				t.Errorf("%s overlay did not change the render", sc.name)
			}

			nm, _ = m.Update(keyEscape)
			m = nm.(appModel)
			if m.help.active {
				t.Fatalf("esc did not close help on %s", sc.name)
			}
			if sc.stable && m.render() != before {
				t.Errorf("%s not restored to its pre-overlay state after close", sc.name)
			}
		})
	}
}

// TestHelpClosesOnQuestion confirms ? toggles the overlay shut, not just esc (AC 1).
func TestHelpClosesOnQuestion(t *testing.T) {
	m := helpApp(t)
	nm, _ := m.Update(keyQuestion)
	m = nm.(appModel)
	if !m.help.active {
		t.Fatal("? did not open help")
	}
	nm, _ = m.Update(keyQuestion)
	if nm.(appModel).help.active {
		t.Error("second ? did not close help")
	}
}

// TestHelpOpensOnRunningAndSummary covers the two dashboard sub-states, both of
// which the ticket lists explicitly (AC 1).
func TestHelpOpensOnRunningAndSummary(t *testing.T) {
	rm, _ := helpApp(t).startRunLoop("")
	running := rm.(appModel)
	running.loopCancel()

	nm, _ := running.Update(keyQuestion)
	if !nm.(appModel).help.active {
		t.Error("? did not open help on the running dashboard")
	}

	sm, _ := running.dash.enterSummary(console.SessionSummary{Tickets: 1, Elapsed: time.Minute, CostMetered: true})
	running.dash = sm.(model)
	nm, _ = running.Update(keyQuestion)
	m := nm.(appModel)
	if !m.help.active {
		t.Fatal("? did not open help on the summary recap")
	}
	if got := ansi.Strip(m.render()); !strings.Contains(got, "Session complete") {
		t.Error("summary overlay should be titled from summaryHelp")
	}
}

// TestHelpOverlayRevealsHiddenKeysAndFilters proves the overlay surfaces bindings
// the static footer hid and that typing narrows the list fuzzily (AC 2).
func TestHelpOverlayRevealsHiddenKeysAndFilters(t *testing.T) {
	am, _ := helpApp(t).selectAction(actLogs)
	m := am.(appModel)

	// The shift+↑↓ half-page binding was never in the logs footer.
	if strings.Contains(ansi.Strip(m.render()), "half-page") {
		t.Fatal("precondition: half-page should be hidden from the footer")
	}

	nm, _ := m.Update(keyQuestion)
	m = nm.(appModel)
	if !strings.Contains(ansi.Strip(m.render()), "half-page") {
		t.Error("overlay should reveal the previously-hidden shift+↑↓ half-page binding")
	}

	for _, r := range "jump" {
		nm, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = nm.(appModel)
	}
	if m.help.filter != "jump" {
		t.Fatalf("filter = %q, want jump", m.help.filter)
	}
	filtered := ansi.Strip(m.render())
	if !strings.Contains(filtered, "jump") {
		t.Error("filtered overlay should still show the matching jump binding")
	}
	if strings.Contains(filtered, "half-page") {
		t.Error("filter should have hidden the non-matching half-page binding")
	}
}

// TestFootersDeriveFromHelp asserts the one-source-of-truth invariant: each
// footer legend is exactly its screenHelp.footer() (AC 3).
func TestFootersDeriveFromHelp(t *testing.T) {
	cases := []struct{ got, want string }{
		{menuHelp().footer(), "↑↓ move · enter select · q quit"},
		{moreHelp().footer(), "↑↓ move · enter select · esc/q back"},
		{resetHelp().footer(), "enter confirm · esc back"},
		{leafHelp("Version").footer(), "esc/q back"},
	}
	for _, c := range cases {
		// footer() weaves in zero-width bubblezone click markers; the visible legend
		// is what must match, so compare stripped.
		if got := ansi.Strip(c.got); got != c.want {
			t.Errorf("footer() = %q, want %q", got, c.want)
		}
	}

	// And the menu actually renders that derived footer.
	m := helpApp(t)
	if !strings.Contains(ansi.Strip(m.render()), ansi.Strip(menuHelp().footer())) {
		t.Error("menu did not render its footer from menuHelp()")
	}
}

// TestEditingGatesHelp confirms ? opens help on a non-text step but is typed as a
// literal while a free-text field is focused (AC 1 edge case).
func TestEditingGatesHelp(t *testing.T) {
	am, _ := helpApp(t).selectAction(actOnboarding)
	m := am.(appModel)

	m.onboard.step = onboardWelcome
	if m.editing() {
		t.Error("welcome step should not gate help")
	}

	m.onboard.step = onboardJiraCreds
	if !m.editing() {
		t.Fatal("jira-creds step has a focused text field and should gate help")
	}
	nm, _ := m.Update(keyQuestion)
	if nm.(appModel).help.active {
		t.Error("? opened help while a free-text field was focused")
	}
}

// TestHelpOverlayScrollsTallList proves a binding list taller than the viewport
// scrolls and stays clamped, and the composite still fits 80×24 (AC 4).
func TestHelpOverlayScrollsTallList(t *testing.T) {
	var keys []helpKey
	for i := 0; i < 40; i++ {
		keys = append(keys, fk(fmt.Sprintf("k%d", i), "does a thing"))
	}
	h := screenHelp{title: "Big", columns: []helpColumn{group("All", keys...)}}
	s := DefaultStyles()

	lay := layoutHelp(s, h, "", 80, 24)
	if lay.viewport >= len(lay.body) {
		t.Fatalf("expected 40 keys to overflow the viewport (viewport=%d body=%d)", lay.viewport, len(lay.body))
	}

	hm := helpModel{active: true}
	hm, _ = hm.update(tea.KeyPressMsg{Code: tea.KeyDown}, lay)
	if hm.offset != 1 {
		t.Errorf("offset after ↓ = %d, want 1", hm.offset)
	}
	hm, _ = hm.update(tea.KeyPressMsg{Code: tea.KeyEnd}, lay)
	if hm.offset != lay.maxOffset() {
		t.Errorf("offset after End = %d, want max %d", hm.offset, lay.maxOffset())
	}
	// Over-scroll then clamp.
	for i := 0; i < 5; i++ {
		hm, _ = hm.update(tea.KeyPressMsg{Code: tea.KeyDown}, lay)
	}
	if hm.offset != lay.maxOffset() {
		t.Errorf("offset over-scrolled to %d, want clamped %d", hm.offset, lay.maxOffset())
	}

	var rows []string
	for i := 0; i < 24; i++ {
		rows = append(rows, strings.Repeat("x", 80))
	}
	over := compositeHelp(s, strings.Join(rows, "\n"), h, hm, 80, 24)
	if got := lipgloss.Height(over); got != 24 {
		t.Errorf("tall overlay height = %d, want 24", got)
	}
	if got := lipgloss.Width(over); got > 80 {
		t.Errorf("tall overlay width = %d, overflows 80", got)
	}
}
