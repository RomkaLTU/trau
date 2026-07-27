// Package browser hands a URL to the host's browser. It is best-effort by
// design: inside WSL there is usually no linux GUI browser at all, so the
// hand-off has to reach the Windows side and can still come up empty. Callers
// show the URL as well, since that is the fallback that always works (ADR 0023).
package browser

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const procVersion = "/proc/version"

// IsWSL reports whether this process is running inside the Windows Subsystem
// for Linux. A WSL kernel carries a "microsoft" build tag in its version string.
func IsWSL() bool { return isWSL(os.Getenv("WSL_DISTRO_NAME"), procVersion) }

func isWSL(distro, versionPath string) bool {
	if distro != "" {
		return true
	}
	data, err := os.ReadFile(versionPath)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}

// Open asks the host to open url, trying each candidate opener until one
// starts.
func Open(url string) error {
	var err error
	for _, argv := range openers(runtime.GOOS, IsWSL(), os.Getenv("BROWSER"), url) {
		cmd := exec.Command(argv[0], argv[1:]...)
		if err = cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			return nil
		}
	}
	return fmt.Errorf("open %s: %w", url, err)
}

// openers lists the invocations to try, most specific first. Under WSL the
// chain ends at wslview — the wslu shim that reaches the Windows shell, which
// is commonly but not universally preinstalled.
func openers(goos string, wsl bool, browserEnv, url string) [][]string {
	switch {
	case goos == "darwin":
		return [][]string{{"open", url}}
	case goos == "windows":
		return [][]string{{"rundll32", "url.dll,FileProtocolHandler", url}}
	case !wsl:
		return [][]string{{"xdg-open", url}}
	}
	chain := make([][]string, 0, 3)
	if browserEnv != "" {
		chain = append(chain, []string{browserEnv, url})
	}
	return append(chain, []string{"xdg-open", url}, []string{"wslview", url})
}
