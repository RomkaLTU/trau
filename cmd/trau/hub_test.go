package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

func TestHubRestartRejectsUnknownArg(t *testing.T) {
	err := runHubRestart(context.Background(), []string{"--yolo"}, io.Discard)

	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("runHubRestart returned %v, want a usage error", err)
	}
}

func TestCheckForcedRestart(t *testing.T) {
	deadPID := 1 << 22
	tests := []struct {
		name    string
		active  string
		entry   *registry.Entry
		wantErr string
	}{
		{
			name:    "inside a managed run",
			active:  "1",
			wantErr: "trau-managed run",
		},
		{
			name:    "a live loop owns the hub",
			entry:   &registry.Entry{PID: os.Getpid(), RepoRoot: "/repos/one", Ticket: "COD-1"},
			wantErr: "COD-1",
		},
		{
			name:  "a loop whose process is gone",
			entry: &registry.Entry{PID: deadPID, RepoRoot: "/repos/one"},
		},
		{
			name: "no hub database yet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TRAU_HOME", home)
			t.Setenv("TRAU_ACTIVE", tt.active)
			if tt.entry != nil {
				seedInstance(t, home, *tt.entry)
			}

			err := checkForcedRestart()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkForcedRestart refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("checkForcedRestart allowed the kill, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("refusal %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestHubRestartPortBusyOffersForce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	t.Setenv("TRAU_SERVE_BIND", "127.0.0.1")
	t.Setenv("TRAU_SERVE_PORT", port)

	err = runHubRestart(context.Background(), nil, io.Discard)

	var a *console.ActionableError
	if !errors.As(err, &a) {
		t.Fatalf("runHubRestart returned %v, want an actionable error", err)
	}
	if !strings.Contains(a.Suggestion, "--force") {
		t.Fatalf("suggestion %q does not offer --force", a.Suggestion)
	}
}

func TestPortListenersNamesTheHolder(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof is not installed")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	pids, err := portListeners(context.Background(), port)
	if err != nil {
		t.Fatalf("portListeners: %v", err)
	}
	if !slices.Contains(pids, os.Getpid()) {
		t.Fatalf("portListeners(%d) = %v, want it to include this process (%d)", port, pids, os.Getpid())
	}
}

func TestStopProcessEndsIt(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})

	if err := stopProcess(pid); err != nil {
		t.Fatalf("stopProcess: %v", err)
	}
	select {
	case <-reaped:
	case <-time.After(time.Second):
		t.Fatalf("pid %d outlived stopProcess", pid)
	}
}

func seedInstance(t *testing.T, home string, e registry.Entry) {
	t.Helper()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub database: %v", err)
	}
	defer func() { _ = db.Close() }()
	e.StartedAt = time.Now().UTC()
	e.Heartbeat = e.StartedAt
	if err := hubstore.NewInstances(db.SQL()).Upsert(e); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
}
