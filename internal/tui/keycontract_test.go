package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/console"
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

// TestWebUIKeyContract pins the global W binding: it fires the Open Web UI
// action on browse screens and over the running dashboard, stays out of
// focused text inputs (W is a valid ticket-id letter), and surfaces the
// backend's refusal instead of silently doing nothing.
func TestWebUIKeyContract(t *testing.T) {
	keyW := tea.KeyPressMsg{Code: 'W', Text: "W"}
	t.Cleanup(func() { setScreenWeb(webStatus{}) })

	newApp := func(t *testing.T) (appModel, *fakeAppActions) {
		t.Helper()
		fake := &fakeAppActions{
			fakeOnboardActions: fakeOnboardActions{repoRoot: t.TempDir()},
			hubBase:            "http://127.0.0.1:8728",
		}
		m := newAppModel(context.Background(), fake, nil)
		nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		return nm.(appModel), fake
	}
	press := func(m appModel, key tea.KeyPressMsg) (appModel, tea.Cmd) {
		nm, cmd := m.Update(key)
		return nm.(appModel), cmd
	}

	t.Run("menu fires open", func(t *testing.T) {
		m, fake := newApp(t)
		_, cmd := press(m, keyW)
		if cmd == nil {
			t.Fatal("W on the menu returned no command")
		}
		raw := cmd()
		msg, ok := raw.(openWebDoneMsg)
		if !ok {
			t.Fatalf("W produced %T, want openWebDoneMsg", raw)
		}
		if fake.openWebCalls != 1 {
			t.Fatalf("OpenWebUI calls = %d, want 1", fake.openWebCalls)
		}
		if msg.err != nil || msg.url != "http://127.0.0.1:8728/" {
			t.Fatalf("unexpected outcome: url=%q err=%v", msg.url, msg.err)
		}
	})

	t.Run("running dashboard fires open", func(t *testing.T) {
		m, fake := newApp(t)
		rm, _ := m.startRunLoop("")
		m = rm.(appModel)
		if m.loopCancel != nil {
			defer m.loopCancel()
		}
		_, cmd := press(m, keyW)
		if cmd == nil {
			t.Fatal("W on the running dashboard returned no command")
		}
		if _, ok := cmd().(openWebDoneMsg); !ok || fake.openWebCalls != 1 {
			t.Fatalf("W did not run OpenWebUI while running (calls = %d)", fake.openWebCalls)
		}
	})

	t.Run("focused input keeps W", func(t *testing.T) {
		m, fake := newApp(t)
		am, _ := m.selectAction(actReset)
		m = am.(appModel)
		m, _ = press(m, keyW)
		if got := m.reset.Value(); got != "W" {
			t.Fatalf("W into focused input = %q, want \"W\"", got)
		}
		if fake.openWebCalls != 0 {
			t.Fatalf("W fired OpenWebUI from a text input (calls = %d)", fake.openWebCalls)
		}
	})

	t.Run("refusal is reported", func(t *testing.T) {
		m, fake := newApp(t)
		fake.openWebErr = errors.New("hub autostart is off (SERVE_AUTOSTART=0) — run 'trau serve'")
		m, cmd := press(m, keyW)
		nm, next := m.Update(cmd())
		m = nm.(appModel)
		if next != nil {
			t.Fatal("a refused open must not still open a browser")
		}
		if !m.hubNoteErr || !strings.Contains(m.hubNote, "SERVE_AUTOSTART=0") {
			t.Fatalf("refusal note = %q (err=%v), want the reason surfaced", m.hubNote, m.hubNoteErr)
		}
		if screenWeb.healthy {
			t.Fatal("indicator still claims a healthy hub after a refused open")
		}
	})

	t.Run("summary screen shows the refusal", func(t *testing.T) {
		m, fake := newApp(t)
		fake.openWebErr = errors.New("hub autostart is off (SERVE_AUTOSTART=0) — run 'trau serve'")
		rm, _ := m.startRunLoop("")
		m = rm.(appModel)
		if m.loopCancel != nil {
			defer m.loopCancel()
		}
		d, _ := m.dash.enterSummary(console.SessionSummary{})
		m.dash = d.(model)

		m, cmd := press(m, keyW)
		nm, _ := m.Update(cmd())
		m = nm.(appModel)
		if plain := ansi.Strip(m.render()); !strings.Contains(plain, "SERVE_AUTOSTART=0") {
			t.Fatalf("a refused open left no message on the summary screen:\n%s", plain)
		}
	})
}
