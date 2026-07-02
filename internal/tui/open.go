package tui

import (
	"os/exec"
	"runtime"

	tea "charm.land/bubbletea/v2"
)

// openURLCmd returns a tea.Cmd that opens url in the user's default browser.
func openURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		var cmd string
		switch runtime.GOOS {
		case "darwin":
			cmd = "open"
		default:
			cmd = "xdg-open"
		}
		_ = exec.Command(cmd, url).Start()
		return nil
	}
}
