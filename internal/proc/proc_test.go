package proc

import (
	"context"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
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

// listenOn holds an ephemeral port on host open for the rest of the test and
// returns it, so PortListeners has this process to find on it. A machine without
// host's address family skips the test rather than failing it.
func listenOn(t *testing.T, host string) int {
	t.Helper()
	ln, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Skipf("listen on %s: %v", host, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return port
}

// TestPortListenersNamesTheHolder exercises the port->pid lookup for real, on
// both address families: Windows netstat lists them as separate rows and a hub
// bound to ::1 has to be as findable as one on 127.0.0.1.
func TestPortListenersNamesTheHolder(t *testing.T) {
	skipWithoutPortTool(t)
	for _, host := range []string{"127.0.0.1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			port := listenOn(t, host)

			pids, err := PortListeners(context.Background(), port)
			if err != nil {
				t.Fatalf("PortListeners: %v", err)
			}
			if !slices.Contains(pids, os.Getpid()) {
				t.Fatalf("PortListeners(%d) = %v, want it to include this process (%d)", port, pids, os.Getpid())
			}
		})
	}
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
