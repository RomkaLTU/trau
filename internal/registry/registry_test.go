package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// deadPID starts a child, kills and reaps it, and returns a PID that is now
// guaranteed not to name a running process.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return pid
}

func TestAliveReportsRunningProcess(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Errorf("Alive(own pid) = false, want true")
	}
	if Alive(deadPID(t)) {
		t.Errorf("Alive(dead pid) = true, want false")
	}
	if Alive(0) {
		t.Errorf("Alive(0) = true, want false")
	}
	if Alive(-1) {
		t.Errorf("Alive(-1) = true, want false")
	}
}

func TestHomePrefersEnvOverDefault(t *testing.T) {
	t.Setenv("TRAU_HOME", "/custom/trau")
	if got := Home(); got != "/custom/trau" {
		t.Errorf("Home() with TRAU_HOME set = %q, want /custom/trau", got)
	}

	t.Setenv("TRAU_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable user home dir")
	}
	if got, want := Home(), filepath.Join(home, ".trau"); got != want {
		t.Errorf("Home() default = %q, want %q", got, want)
	}
}
