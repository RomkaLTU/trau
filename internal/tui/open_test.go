package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The opener is best-effort — under WSL there is often none — so the URL itself
// is what the user is left to copy.
func TestOpenedURLShowsInShell(t *testing.T) {
	t.Cleanup(func() { setScreenWeb(webStatus{}) })
	am := newAppModel(context.Background(), &fakeAppActions{}, nil)
	nm, _ := am.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	nm, _ = nm.(appModel).Update(openedURLMsg{url: "http://127.0.0.1:8728/"})

	if plain := ansi.Strip(nm.(appModel).render()); !strings.Contains(plain, "http://127.0.0.1:8728/") {
		t.Fatalf("shell dropped the handed-off URL:\n%s", plain)
	}
}

// The live dashboard draws its own toast instead of the shell's overlay.
func TestOpenedURLShowsInDashboardToast(t *testing.T) {
	nm, _ := freshDash(100, 30, "main").Update(openedURLMsg{url: "http://127.0.0.1:8728/"})

	if plain := ansi.Strip(nm.(model).render()); !strings.Contains(plain, "http://127.0.0.1:8728/") {
		t.Fatalf("dashboard dropped the handed-off URL:\n%s", plain)
	}
}
