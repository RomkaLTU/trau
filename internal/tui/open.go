package tui

import (
	"os/exec"
	"runtime"

	tea "charm.land/bubbletea/v2"
)

// openURLCmd returns a tea.Cmd that opens url in the user's default browser.
func openURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		switch runtime.GOOS {
		case "darwin":
			_ = exec.Command("open", url).Start()
		case "windows":
			_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		default:
			_ = exec.Command("xdg-open", url).Start()
		}
		return nil
	}
}
