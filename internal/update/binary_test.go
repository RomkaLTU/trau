package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveBinaryPrefersRunningPath checks a binary that is still on disk
// wins, so a dev build outside PATH re-execs itself rather than an installed one.
func TestResolveBinaryPrefersRunningPath(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "trau")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	got, err := resolveBinaryFrom(exe)
	if err != nil {
		t.Fatalf("resolveBinaryFrom: %v", err)
	}
	if got != exe {
		t.Fatalf("resolved %q, want the running binary %q", got, exe)
	}
}

// TestResolveBinaryFallsBackToPath covers the cask upgrade: the versioned
// Caskroom directory the running process lives in is deleted, so resolution has
// to find the freshly installed binary on PATH instead.
func TestResolveBinaryFallsBackToPath(t *testing.T) {
	installed := filepath.Join(t.TempDir(), "trau")
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(installed))

	got, err := resolveBinaryFrom(filepath.Join(t.TempDir(), "Caskroom", "trau", "2.1.0", "trau"))
	if err != nil {
		t.Fatalf("resolveBinaryFrom: %v", err)
	}
	if got != installed {
		t.Fatalf("resolved %q, want the binary on PATH %q", got, installed)
	}
}

// TestResolveBinaryWithoutAnyBinary checks resolution fails loudly when the
// running path is gone and PATH has no replacement, so a restart reports why it
// cannot spawn a successor instead of exiting silently.
func TestResolveBinaryWithoutAnyBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	gone := filepath.Join(t.TempDir(), "Caskroom", "trau", "2.1.0", "trau")
	if _, err := resolveBinaryFrom(gone); err == nil {
		t.Fatal("resolveBinaryFrom succeeded with no binary anywhere")
	}
}

// TestPreflightResult pins what the two failing answers are told apart by: a
// build that predates the subcommand names it, and everything else that fails is
// a build that cannot serve — a panic on startup included, which leaves with the
// same exit code a usage error does.
func TestPreflightResult(t *testing.T) {
	const path = "/opt/homebrew/bin/trau"
	exit2 := errors.New("exit status 2")

	tests := []struct {
		name        string
		out         string
		err         error
		unsupported bool
	}{
		{name: "can serve", out: "trau 2.16.0 can serve: hub schema v55, transcripts v1\n"},
		{name: "predates the preflight", out: "hub: unknown subcommand: preflight\n", err: exit2, unsupported: true},
		{name: "panics on startup", out: "panic: runtime error: index out of range\n\ngoroutine 1 [running]:\n", err: exit2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := preflightResult(path, []byte(tt.out), tt.err)
			if (err != nil) != (tt.err != nil) {
				t.Fatalf("preflightResult = %v, want an error: %v", err, tt.err != nil)
			}
			if got := errors.Is(err, ErrPreflightUnsupported); got != tt.unsupported {
				t.Errorf("unsupported = %v, want %v", got, tt.unsupported)
			}
		})
	}
}

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"release", "trau 2.2.0\n", "2.2.0"},
		{"dev", "trau dev\n", "dev"},
		{"no trailing newline", "trau 2.2.0", "2.2.0"},
		{"leading noise", "warning: stale config\ntrau 2.2.0\n", "2.2.0"},
		{"empty", "", ""},
		{"another binary", "goloop 1.0.0\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseVersionOutput(tt.out); got != tt.want {
				t.Errorf("parseVersionOutput(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

func TestClassifyInstall(t *testing.T) {
	anyManager := func(string) bool { return true }
	noManager := func(string) bool { return false }

	tests := []struct {
		name   string
		target string
		onPath func(string) bool
		want   string
	}{
		{"cask", "/opt/homebrew/Caskroom/trau/2.2.0/trau", anyManager, installBrew},
		{"cellar", "/opt/homebrew/Cellar/trau/2.2.0/bin/trau", anyManager, installBrew},
		{"intel cask", "/usr/local/Caskroom/trau/2.2.0/trau", anyManager, installBrew},
		{"cask path without brew", "/opt/homebrew/Caskroom/trau/2.2.0/trau", noManager, installOther},
		{"scoop app", `C:\Users\rd\scoop\apps\trau\2.2.0\trau.exe`, anyManager, installScoop},
		{"scoop shim", `C:\Users\rd\scoop\shims\trau.exe`, anyManager, installScoop},
		{"scoop path without scoop", `C:\Users\rd\scoop\shims\trau.exe`, noManager, installOther},
		{"winget package", `C:\Users\rd\AppData\Local\Microsoft\WinGet\Packages\Codesomelabs.trau_x\trau.exe`, anyManager, installWinget},
		{"winget path without winget", `C:\Users\rd\AppData\Local\Microsoft\WinGet\Links\trau.exe`, noManager, installOther},
		{"go install", "/Users/rd/go/bin/trau", anyManager, installOther},
		{"dev build", "/Users/rd/Projects/loop/bin/trau", anyManager, installOther},
		{"windows dev build", `C:\Users\rd\Projects\loop\bin\trau.exe`, anyManager, installOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyInstall(tt.target, tt.onPath); got != tt.want {
				t.Errorf("classifyInstall(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

// TestClassifyInstallChecksItsOwnManager pins that the PATH probe asks for the
// manager the path named, not brew: a Windows box has no brew to find.
func TestClassifyInstallChecksItsOwnManager(t *testing.T) {
	asked := []string{}
	onPath := func(name string) bool {
		asked = append(asked, name)
		return true
	}

	if got := classifyInstall(`C:\Users\rd\scoop\shims\trau.exe`, onPath); got != installScoop {
		t.Fatalf("classifyInstall = %q, want %q", got, installScoop)
	}
	if len(asked) != 1 || asked[0] != installScoop {
		t.Errorf("probed %v on PATH, want just %q", asked, installScoop)
	}
}

func TestUpgradeCommand(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{installBrew, "brew upgrade --cask trau"},
		{installScoop, "scoop update trau"},
		{installWinget, "winget upgrade Codesomelabs.trau"},
		{installOther, ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := upgradeCommand(tt.method); got != tt.want {
			t.Errorf("upgradeCommand(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}
