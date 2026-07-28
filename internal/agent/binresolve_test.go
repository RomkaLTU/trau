package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeProbeExe creates a runnable file named base in dir — with the extension
// Windows needs before it will consider it executable — and returns the path a
// lookup of base should land on.
func writeProbeExe(t *testing.T, dir, base string) string {
	t.Helper()
	name, body, mode := base, []byte("#!/bin/sh\nexit 0\n"), os.FileMode(0o755)
	if runtime.GOOS == "windows" {
		name, body, mode = base+".bat", []byte("@echo off\r\nexit /b 0\r\n"), 0o644
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("write probe executable: %v", err)
	}
	return path
}

// The regression this guards: go-pty resolves a bare name against the child's
// working directory rather than PATH, so trau has to hand it an already-resolved
// absolute path. A binary on PATH must therefore resolve even when the process
// sits in an unrelated directory that holds nothing by that name.
func TestResolveBinFindsABareNameOnPATHNotInTheWorkingDirectory(t *testing.T) {
	binDir := t.TempDir()
	want := writeProbeExe(t, binDir, "trau-resolve-probe")
	t.Setenv("PATH", binDir)
	t.Chdir(t.TempDir())

	got, err := resolveBin("trau-resolve-probe")
	if err != nil {
		t.Fatalf("resolveBin: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveBin = %q, want an absolute path", got)
	}
	if got != want {
		t.Errorf("resolveBin = %q, want %q", got, want)
	}
}

// go-pty rebuilds its argv0 from the path it is handed, so the extension PATHEXT
// supplied has to survive resolution — an extensionless absolute path fails on
// Windows even with a matching executable beside it.
func TestResolveBinKeepsTheExtensionPATHEXTSupplied(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT resolution is Windows-only")
	}
	binDir := t.TempDir()
	want := writeProbeExe(t, binDir, "trau-resolve-ext")
	t.Setenv("PATH", binDir)

	got, err := resolveBin("trau-resolve-ext")
	if err != nil {
		t.Fatalf("resolveBin: %v", err)
	}
	if got != want {
		t.Errorf("resolveBin = %q, want %q — the resolved extension must be kept", got, want)
	}
}

func TestResolveBinAbsolutePathPassesThrough(t *testing.T) {
	binDir := t.TempDir()
	want := writeProbeExe(t, binDir, "trau-resolve-abs")

	got, err := resolveBin(want)
	if err != nil {
		t.Fatalf("resolveBin: %v", err)
	}
	if got != want {
		t.Errorf("resolveBin = %q, want %q", got, want)
	}
}

func TestResolveBinRejectsEmptyAndMissing(t *testing.T) {
	if _, err := resolveBin(""); err == nil {
		t.Error(`resolveBin("") = nil error, want a configuration error`)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveBin("trau-definitely-not-installed"); err == nil {
		t.Error("resolveBin on a binary that is not on PATH = nil error, want a lookup error")
	}
}
