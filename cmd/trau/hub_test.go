package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubdb/hubdbtest"
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

// TestHubPreflightOpensTheDatabases covers what a hub asks a candidate build
// before restarting onto it: this binary's migrations apply and both databases
// open, so it would reach the point of serving rather than dying at startup.
func TestHubPreflightOpensTheDatabases(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".trau")
	t.Setenv("TRAU_HOME", home)

	var out strings.Builder
	if err := runHubPreflight(nil, &out); err != nil {
		t.Fatalf("runHubPreflight: %v", err)
	}

	if !strings.Contains(out.String(), "can serve") {
		t.Errorf("output = %q, want it to report the binary can serve", out.String())
	}
	if _, err := os.Stat(hubdb.Path(home)); err != nil {
		t.Errorf("hub database: %v", err)
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

// TestSupervisionHookRewritesThePlistToTheChosenBuild covers what a channel
// switch hands launchd: the agent names the build the switch picked, while the
// environment and the log paths `trau hub supervise` writes come along
// unchanged, so the successor starts the way an installed one does.
func TestSupervisionHookRewritesThePlistToTheChosenBuild(t *testing.T) {
	if !launchd.Supported() {
		t.Skip("launchd is macOS only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TRAU_HOME", filepath.Join(home, ".trau"))
	t.Setenv("PATH", "/opt/homebrew/bin:/usr/bin")

	t.Setenv("TRAU_SUPERVISED", "")
	if supervisionHook() != nil {
		t.Error("a hub launchd does not own was handed an agent to rewrite")
	}

	t.Setenv("TRAU_SUPERVISED", "1")
	hook := supervisionHook()
	if hook == nil {
		t.Fatal("a supervised hub was left with no way to move its agent")
	}
	if err := hook("/src/acme/bin/trau"); err != nil {
		t.Fatalf("rewrite the plist: %v", err)
	}

	path, err := launchd.PlistPath()
	if err != nil {
		t.Fatalf("plist path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the rewritten plist: %v", err)
	}
	for _, want := range []string{
		"<string>/src/acme/bin/trau</string>",
		"<string>serve</string>",
		"<key>TRAU_SUPERVISED</key>\n\t\t<string>1</string>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/bin</string>",
		"<string>" + filepath.Join(home, ".trau", "hub.log") + "</string>",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the rewritten plist is missing %q:\n%s", want, raw)
		}
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
	db, err := hubdbtest.Open(home)
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
