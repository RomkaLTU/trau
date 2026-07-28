package agent

import (
	"errors"
	"os/exec"
	"path/filepath"
)

// resolveBin turns an agent CLI reference into an absolute executable path before
// it reaches the PTY layer.
//
// go-pty's Windows Start carries a copy of Go's pre-1.19 os/exec path helpers, and
// with cmd.Dir set — which every interactive run sets, to the repo root — it
// resolves a bare name against that directory instead of PATH. A configured
// "claude" was therefore only ever found when the binary happened to sit inside
// the repo, so every interactive run on native Windows failed with
// `exec: "<repo>\claude": executable file not found in %PATH%` while the
// preflight and doctor — both on exec.LookPath — reported the provider present.
// Resolving here puts the whole process on that one resolver, which honours PATH
// and PATHEXT, and hands go-pty a path its joinExeDirAndFName returns unchanged.
//
// The extension LookPath found is kept rather than trimmed back to the configured
// name: go-pty rebuilds its argv0 from the path it is given, so an extensionless
// absolute path still fails on Windows even with a matching .exe beside it.
//
// On unix this is a no-op beyond making the path absolute.
func resolveBin(bin string) (string, error) {
	if bin == "" {
		return "", errors.New("no agent binary configured")
	}
	lp, err := exec.LookPath(bin)
	// ErrDot reports only that the match came from the current directory; the
	// path is usable, and the absolute form below makes that origin explicit.
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return "", err
	}
	return filepath.Abs(lp)
}
