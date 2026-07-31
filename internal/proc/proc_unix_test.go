//go:build !windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestStopGracefullyEndsTheProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if err := StopGracefully(pid); err != nil {
		t.Fatalf("StopGracefully(%d): %v", pid, err)
	}
	if err := cmd.Wait(); err == nil {
		t.Error("child exited cleanly, want the signalled exit SIGTERM produces")
	}
	if Alive(pid) {
		t.Errorf("Alive(%d) = true after StopGracefully, want false", pid)
	}
}

func TestStopGracefullyReportsAnAlreadyExitedProcess(t *testing.T) {
	if err := StopGracefully(deadPID(t)); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("StopGracefully(dead pid) = %v, want %v", err, os.ErrProcessDone)
	}
}

func skipWithoutPortTool(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof is not installed")
	}
}

// LookBin is the identity on unix — even for a name that does not exist. The
// stdlib exec inside the PTY spawn does the $PATH resolution at start, and
// resolving earlier would change the argv[0] the child sees (ADR 0023).
func TestLookBinReturnsTheNameUnchanged(t *testing.T) {
	got, err := LookBin("trau-test-no-such-bin")
	if err != nil || got != "trau-test-no-such-bin" {
		t.Errorf("LookBin = %q, %v; want the name back unchanged with no error", got, err)
	}
}
