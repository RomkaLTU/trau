package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/logger"
)

// ResolveBinary picks the trau binary on disk that stands in for the running
// process: the one a restart re-execs, and the one an update check inspects.
func ResolveBinary() (string, error) {
	exe, _ := os.Executable()
	return resolveBinaryFrom(exe)
}

// resolveBinaryFrom resolves exe, the running process's own path, to a binary
// that still exists. exe wins when it does — that covers dev builds outside
// PATH, and the stable /opt/homebrew/bin/trau symlink, which after an upgrade
// already points at the new version. `brew upgrade --cask trau` deletes the old
// versioned Caskroom directory, so a process whose path led into it has nothing
// to re-exec and falls back to whatever `trau` resolves to on PATH now.
func resolveBinaryFrom(exe string) (string, error) {
	if exe != "" {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	}
	path, err := exec.LookPath("trau")
	if err != nil {
		return "", fmt.Errorf("no trau binary to run: %q is gone: %w", exe, err)
	}
	return path, nil
}

// probeBinary reads the version of the trau binary on disk and how it was
// installed. The version is empty when the binary cannot be resolved or run, so
// an unreadable binary claims no drift rather than guessing at one.
func probeBinary() (version, method string) {
	path, err := ResolveBinary()
	if err != nil {
		logger.Debugf("update: resolve trau binary: %v", err)
		return "", installOther
	}
	method = installMethod(path)
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		logger.Debugf("update: %s --version: %v", path, err)
		return "", method
	}
	return parseVersionOutput(string(out)), method
}

// parseVersionOutput reads the version out of `trau --version`, whose only line
// is "trau <version>".
func parseVersionOutput(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "trau "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

const (
	installBrew  = "brew"
	installOther = "other"
)

// installMethod classifies how the binary at path was installed. It resolves
// symlinks first: the stable /opt/homebrew/bin/trau entry is what a user's PATH
// holds, and only its target names the manager that put it there.
func installMethod(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		target = path
	}
	_, err = exec.LookPath("brew")
	return classifyInstall(target, err == nil)
}

func classifyInstall(target string, brewAvailable bool) string {
	if !brewAvailable {
		return installOther
	}
	if strings.Contains(target, "/Caskroom/") || strings.Contains(target, "/Cellar/") {
		return installBrew
	}
	return installOther
}
