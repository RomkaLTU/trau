package update

import (
	"context"
	"errors"
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
	version, err = ProbeVersion(context.Background(), path)
	if err != nil {
		logger.Debugf("update: %v", err)
		return "", method
	}
	return version, method
}

// ProbeVersion runs the trau binary at path and returns the version it reports.
// A binary that cannot be executed, or whose output names no version, is not a
// usable trau.
func ProbeVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", path, err)
	}
	version := parseVersionOutput(string(out))
	if version == "" {
		return "", fmt.Errorf("%s --version: reported no version", path)
	}
	return version, nil
}

// preflightTailLines is how much of a failed preflight's output the error keeps
// — enough for the reason the binary cannot serve, not its whole startup log.
const preflightTailLines = 10

// unknownPreflight is how cmd/trau turned `hub preflight` away in every release
// before the subcommand existed, and so the only answer those builds can give it.
const unknownPreflight = "unknown subcommand: preflight"

// ErrPreflightUnsupported reports a binary too old to have a preflight to run.
// Every trau shipped before the subcommand existed answers it as unknown, and
// they are the installs the release direction restarts onto — refusing them
// would close the way off a dev build (ADR 0024 §2).
var ErrPreflightUnsupported = errors.New("this build predates hub preflight")

// ProbePreflight runs the hub's startup preflight on the trau binary at path.
// --version returns before any of the work that opens the hub databases, so it
// proves only that the binary parses arguments; a build whose migrations collide
// or whose schema will not apply passes it and then dies at startup.
func ProbePreflight(ctx context.Context, path string) error {
	out, err := exec.CommandContext(ctx, path, "hub", "preflight").CombinedOutput()
	return preflightResult(path, out, err)
}

// preflightResult reads the candidate's answer, keeping the tail of its output on
// a failure: why the candidate cannot serve is the whole point of asking. A
// binary with no preflight to run is recognised by its own words and not by the
// usage exit code that comes with them — Go's runtime exits 2 on an unrecovered
// panic too, so the code alone would read a build that crashes at startup as one
// that merely predates the subcommand.
func preflightResult(path string, out []byte, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), unknownPreflight) {
		return fmt.Errorf("%s hub preflight: %w", path, ErrPreflightUnsupported)
	}
	if reason := tail(string(out), preflightTailLines); reason != "" {
		return fmt.Errorf("%s hub preflight: %w\n%s", path, err, reason)
	}
	return fmt.Errorf("%s hub preflight: %w", path, err)
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
	installBrew   = "brew"
	installScoop  = "scoop"
	installWinget = "winget"
	installOther  = "other"
)

// upgradeCommands is what a user runs to update an install the named manager
// owns. An install no manager owns has no entry, and the release page is the
// only honest answer for it.
var upgradeCommands = map[string]string{
	installBrew:   "brew upgrade --cask trau",
	installScoop:  "scoop update trau",
	installWinget: "winget upgrade Codesomelabs.trau",
}

func upgradeCommand(method string) string {
	return upgradeCommands[method]
}

// installMethod classifies how the binary at path was installed. It resolves
// symlinks first: the stable /opt/homebrew/bin/trau entry — and the scoop shim
// and winget link that stand in for it on Windows — is what a user's PATH
// holds, and only its target names the manager that put it there.
func installMethod(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		target = path
	}
	return classifyInstall(target, managerOnPath)
}

func managerOnPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// classifyInstall names the manager whose install layout target sits in, and
// only while that manager is still on PATH. The Homebrew prefixes are fixed,
// while Windows paths reach here in whatever case and separator the caller
// used.
func classifyInstall(target string, onPath func(name string) bool) string {
	slashed := strings.ReplaceAll(target, `\`, "/")
	lower := strings.ToLower(slashed)

	manager := installOther
	switch {
	case strings.Contains(slashed, "/Caskroom/") || strings.Contains(slashed, "/Cellar/"):
		manager = installBrew
	case strings.Contains(lower, "/scoop/"):
		manager = installScoop
	case strings.Contains(lower, "/winget/"):
		manager = installWinget
	}
	if manager == installOther || !onPath(manager) {
		return installOther
	}
	return manager
}
