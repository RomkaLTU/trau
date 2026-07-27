package proc

import (
	"os"
	"os/exec"
	"testing"
)

// deadPID starts the test binary with a filter that matches no test, waits for it
// to exit, and returns its now-reaped pid — a pid guaranteed not to name a
// running process, on any platform.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for child: %v", err)
	}
	return pid
}

func TestAlive(t *testing.T) {
	cases := []struct {
		name string
		pid  int
		want bool
	}{
		{name: "own process", pid: os.Getpid(), want: true},
		{name: "exited process", pid: deadPID(t), want: false},
		{name: "zero", pid: 0, want: false},
		{name: "negative", pid: -1, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Alive(tc.pid); got != tc.want {
				t.Errorf("Alive(%d) = %v, want %v", tc.pid, got, tc.want)
			}
		})
	}
}
