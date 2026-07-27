package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/browser"
)

// openedURLMsg reports a completed hand-off attempt. The opener is best-effort
// — inside WSL there may be none at all — so the shell shows the URL itself.
type openedURLMsg struct{ url string }

// openURLCmd returns a tea.Cmd that opens url in the user's default browser.
func openURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		_ = browser.Open(url)
		return openedURLMsg{url: url}
	}
}

func openedURLLine(url string) string { return "→ " + url }
