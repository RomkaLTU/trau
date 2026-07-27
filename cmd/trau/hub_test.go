package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/launchd"
	"github.com/RomkaLTU/trau/internal/registry"
)

func TestHubRestartRejectsUnknownArg(t *testing.T) {
	err := runHubRestart(context.Background(), []string{"--yolo"}, io.Discard)

	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("runHubRestart returned %v, want a usage error", err)
	}
}

func TestHubSuperviseRejectsUnknownArgs(t *testing.T) {
	for _, args := range [][]string{{"--now"}, {"--force"}} {
		var ue usageError
		if err := runHubSupervise(context.Background(), args, io.Discard); !errors.As(err, &ue) {
			t.Errorf("runHubSupervise(%v) = %v, want a usage error", args, err)
		}
		if err := runHubUnsupervise(args, io.Discard); !errors.As(err, &ue) {
			t.Errorf("runHubUnsupervise(%v) = %v, want a usage error", args, err)
		}
	}
}

// TestHubUnsuperviseWithoutAnAgentIsANoOp keeps the removal safe to run blind:
// a machine that never supervised the hub is told so and left untouched.
func TestHubUnsuperviseWithoutAnAgentIsANoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out strings.Builder
	if err := runHubUnsupervise(nil, &out); err != nil {
		t.Fatalf("runHubUnsupervise: %v", err)
	}
	if !strings.Contains(out.String(), "not supervised") {
		t.Errorf("output = %q, want it to say the hub is not supervised", out.String())
	}
}

// TestHubSuperviseGuardsReleasingTheAgent covers the re-run that adopts a moved
// binary: releasing the agent boots the hub under it out, so the same refusals a
// forced restart answers to have to be reached before the plist is touched.
func TestHubSuperviseGuardsReleasingTheAgent(t *testing.T) {
	if !launchd.Supported() {
		t.Skip("launchd is macOS only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TRAU_HOME", filepath.Join(home, ".trau"))
	t.Setenv("TRAU_ACTIVE", "1")
	t.Setenv("TRAU_SERVE_BIND", "127.0.0.1")
	t.Setenv("TRAU_SERVE_PORT", freePort(t))
	plist := seedPlist(t)

	err := runHubSupervise(context.Background(), nil, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "trau-managed run") {
		t.Fatalf("runHubSupervise = %v, want a refusal naming the managed run", err)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("the refused re-run released the agent anyway: %v", err)
	}
}

// TestSuperviseEnvCarriesPathAndMarker covers what launchd would otherwise strip:
// an agent starts with almost no environment, so the PATH every hub-spawned loop
// resolves git, gh, and the provider CLIs through has to ride in the plist, and
// the marker is what stops a self-restart racing KeepAlive for the port.
func TestSuperviseEnvCarriesPathAndMarker(t *testing.T) {
	t.Setenv("PATH", "/opt/homebrew/bin:/usr/bin")
	t.Setenv("TRAU_HOME", "")

	env := superviseEnv()
	if env["TRAU_SUPERVISED"] != "1" {
		t.Errorf("TRAU_SUPERVISED = %q, want 1", env["TRAU_SUPERVISED"])
	}
	if env["PATH"] != "/opt/homebrew/bin:/usr/bin" {
		t.Errorf("PATH = %q, want the installing shell's", env["PATH"])
	}
	if _, ok := env["TRAU_HOME"]; ok {
		t.Error("an unset TRAU_HOME must not pin the agent to an empty home")
	}

	t.Setenv("TRAU_HOME", "/tmp/iso/.trau")
	if got := superviseEnv()["TRAU_HOME"]; got != "/tmp/iso/.trau" {
		t.Errorf("TRAU_HOME = %q, want it carried into the agent", got)
	}
}

func TestSupervisedHubSkipsItsOwnRespawn(t *testing.T) {
	t.Setenv("TRAU_SUPERVISED", "")
	if supervisedHub() {
		t.Error("an unmarked hub respawns its own successor")
	}
	t.Setenv("TRAU_SUPERVISED", "1")
	if !supervisedHub() {
		t.Error("a launchd-owned hub must leave the successor to KeepAlive")
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

func seedPlist(t *testing.T) string {
	t.Helper()
	path, err := launchd.PlistPath()
	if err != nil {
		t.Fatalf("plist path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	_ = ln.Close()
	return port
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
