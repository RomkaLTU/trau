// Package notify posts desktop notifications for the events in an AFK trau run
// that need a human back at the keyboard. It shells out to the host's native
// mechanism and fails closed: an unsupported platform or a missing backend is a
// silent no-op, never an error the caller has to handle.
package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Notifier posts a desktop notification with the given title and body. A nil
// Notifier means notifications are disabled — callers guard on nil rather than
// invoking it. Errors are advisory only.
type Notifier func(title, body string) error

// OS returns a Notifier backed by the host's desktop notification mechanism —
// osascript on macOS, notify-send on Linux. On a platform with no known backend,
// or when the backend binary is absent, the returned Notifier does nothing.
func OS() Notifier {
	return func(title, body string) error {
		name, args, ok := command(runtime.GOOS, title, body)
		if !ok {
			return nil
		}
		if _, err := exec.LookPath(name); err != nil {
			return nil
		}
		return exec.Command(name, args...).Run()
	}
}

// command builds the host notifier invocation for goos, or ok=false when the
// platform has no supported backend.
func command(goos, title, body string) (name string, args []string, ok bool) {
	title, body = oneLine(title), oneLine(body)
	switch goos {
	case "darwin":
		script := fmt.Sprintf("display notification %s with title %s",
			appleScriptString(body), appleScriptString(title))
		return "osascript", []string{"-e", script}, true
	case "linux":
		return "notify-send", []string{"--", title, body}, true
	default:
		return "", nil, false
	}
}

// oneLine flattens s to a single line, collapsing any whitespace run (newlines,
// tabs) to one space. A desktop notification is a one-line surface, and an
// embedded newline is a syntax error inside an AppleScript string literal — which
// would make osascript exit non-zero and silently drop the notification.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// appleScriptString renders s as a quoted AppleScript string literal, escaping
// backslashes and double quotes so a ticket id or failure reason can't break out
// of the literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
