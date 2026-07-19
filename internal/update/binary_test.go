package update

import (
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
	tests := []struct {
		name          string
		target        string
		brewAvailable bool
		want          string
	}{
		{"cask", "/opt/homebrew/Caskroom/trau/2.2.0/trau", true, installBrew},
		{"cellar", "/opt/homebrew/Cellar/trau/2.2.0/bin/trau", true, installBrew},
		{"intel cask", "/usr/local/Caskroom/trau/2.2.0/trau", true, installBrew},
		{"cask path without brew", "/opt/homebrew/Caskroom/trau/2.2.0/trau", false, installOther},
		{"go install", "/Users/rd/go/bin/trau", true, installOther},
		{"dev build", "/Users/rd/Projects/loop/bin/trau", true, installOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyInstall(tt.target, tt.brewAvailable); got != tt.want {
				t.Errorf("classifyInstall(%q, %v) = %q, want %q", tt.target, tt.brewAvailable, got, tt.want)
			}
		})
	}
}
