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
