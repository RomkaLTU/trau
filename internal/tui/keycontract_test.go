package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestKeyContractPerScreen pins the shared key contract: esc backs out one
// level everywhere (a no-op on the top-level menu), q backs out on leaf screens
// but quits from the top-level menu and types into focused text inputs, and
// enter acts on the selection — it never navigates back.
func TestKeyContractPerScreen(t *testing.T) {
	keyEsc := tea.KeyPressMsg{Code: tea.KeyEsc}
	keyEnter := tea.KeyPressMsg{Code: tea.KeyEnter}
	keyQ := tea.KeyPressMsg{Code: 'q', Text: "q"}

	newApp := func(t *testing.T) appModel {
		t.Helper()
		fake := &fakeAppActions{fakeOnboardActions: fakeOnboardActions{repoRoot: t.TempDir()}}
		m := newAppModel(context.Background(), fake, nil)
		nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		return nm.(appModel)
	}
	open := func(from appView, a menuAction) func(*testing.T) appModel {
		return func(t *testing.T) appModel {
			t.Helper()
			m := newApp(t)
			m.view = from
			nm, _ := m.selectAction(a)
			return nm.(appModel)
		}
	}
	press := func(m appModel, key tea.KeyPressMsg) (appModel, tea.Cmd) {
		nm, cmd := m.Update(key)
		return nm.(appModel), cmd
	}
	isQuit := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}

	cases := []struct {
		name  string
		setup func(*testing.T) appModel
		// afterEsc is the view esc lands on (the screen itself when esc is a no-op).
		afterEsc appView
		// Exactly one q expectation applies: quitsOnQ on the top-level menu,
		// typesQInto when a focused text input owns the key, afterQ otherwise.
		quitsOnQ   bool
		typesQInto func(appModel) string
		afterQ     appView
		// afterEnter is where enter's action leaves the shell — never the parent.
		afterEnter appView
	}{
		{
			name:       "menu",
			setup:      newApp,
			afterEsc:   viewMenu,
			quitsOnQ:   true,
			afterEnter: viewRunLoop,
		},
		{
			name:       "more",
			setup:      open(viewMenu, actMore),
			afterEsc:   viewMenu,
			afterQ:     viewMenu,
			afterEnter: viewStatus,
		},
		{
			name:       "settings",
			setup:      open(viewMore, actSettings),
			afterEsc:   viewMore,
			afterQ:     viewMore,
			afterEnter: viewSettings,
		},
		{
			name:       "status",
			setup:      open(viewMore, actStatus),
			afterEsc:   viewMore,
			afterQ:     viewMore,
			afterEnter: viewStatus,
		},
		{
			name:       "logs",
			setup:      open(viewMore, actLogs),
			afterEsc:   viewMore,
			afterQ:     viewMore,
			afterEnter: viewLogs,
		},
		{
			name:  "loop setup",
			setup: open(viewMenu, actRun),
			// A blank-input enter arms the whole-ready-queue confirm rather than
			// starting, so one enter stays on the screen (armed). The full
			// arm→confirm→start path is covered by TestRunLoopBlankEnterConfirms.
			afterEsc:   viewMenu,
			typesQInto: func(m appModel) string { return m.loopSetup.input.Value() },
			afterEnter: viewRunLoop,
		},
		{
			name:       "run once",
			setup:      open(viewMenu, actRunOnce),
			afterEsc:   viewMenu,
			typesQInto: func(m appModel) string { return m.runOnce.input.Value() },
			afterEnter: viewRunOnce,
		},
		{
			name:       "reset",
			setup:      open(viewMore, actReset),
			afterEsc:   viewMore,
			typesQInto: func(m appModel) string { return m.reset.Value() },
			afterEnter: viewReset,
		},
		{
			name:       "onboarding",
			setup:      open(viewMore, actOnboarding),
			afterEsc:   viewMore,
			afterQ:     viewMore,
			afterEnter: viewOnboarding,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, cmd := press(c.setup(t), keyEsc)
			if isQuit(cmd) {
				t.Error("esc must never quit")
			}
			if m.view != c.afterEsc {
				t.Errorf("esc: view = %d, want %d", m.view, c.afterEsc)
			}

			start := c.setup(t)
			m, cmd = press(start, keyQ)
			switch {
			case c.quitsOnQ:
				if !isQuit(cmd) {
					t.Error("q on the top-level menu must quit")
				}
			case c.typesQInto != nil:
				if m.view != start.view {
					t.Errorf("q: view = %d, want to stay on %d", m.view, start.view)
				}
				if got := c.typesQInto(m); got != "q" {
					t.Errorf("q into focused input = %q, want \"q\"", got)
				}
			default:
				if isQuit(cmd) {
					t.Error("q must go back, not quit")
				}
				if m.view != c.afterQ {
					t.Errorf("q: view = %d, want %d", m.view, c.afterQ)
				}
			}

			m, cmd = press(c.setup(t), keyEnter)
			if isQuit(cmd) {
				t.Error("enter must never quit")
			}
			if m.view != c.afterEnter {
				t.Errorf("enter: view = %d, want %d", m.view, c.afterEnter)
			}
			if m.loopCancel != nil {
				m.loopCancel()
			}
		})
	}
}
