package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// TestResolveRepoErrorNamesRunningHub checks a resolve failure with a healthy
// hub on the configured port points the suggestion at the web UI's URL.
func TestResolveRepoErrorNamesRunningHub(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != webserver.APIPrefix+"/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(webserver.Health{Status: "ok", Version: version})
	}))
	defer ts.Close()

	host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	cfg := config.Config{ServeBind: host, ServePort: port}

	cause := errors.New("not inside a git repository")
	got := resolveRepoError(context.Background(), cfg, cause)

	var a *console.ActionableError
	if !errors.As(got, &a) {
		t.Fatalf("resolveRepoError returned %T, want *console.ActionableError", got)
	}
	if !errors.Is(got, cause) {
		t.Fatalf("resolveRepoError lost the cause: %v", got)
	}
	wantURL := "http://" + host + ":" + portStr + "/"
	if !strings.Contains(a.Suggestion, "web UI is running at "+wantURL) {
		t.Fatalf("suggestion %q does not name the hub URL %s", a.Suggestion, wantURL)
	}
}

// TestResolveRepoErrorWithoutHub checks the plain resolve hint survives when no
// hub answers on the configured port.
func TestResolveRepoErrorWithoutHub(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cfg := config.Config{ServeBind: "127.0.0.1", ServePort: port}

	got := resolveRepoError(context.Background(), cfg, errors.New("not inside a git repository"))

	var a *console.ActionableError
	if !errors.As(got, &a) {
		t.Fatalf("resolveRepoError returned %T, want *console.ActionableError", got)
	}
	if strings.Contains(a.Suggestion, "web UI") {
		t.Fatalf("suggestion %q claims a web UI with no hub up", a.Suggestion)
	}
	if !strings.Contains(a.Suggestion, "--repo <path>") {
		t.Fatalf("suggestion %q lost the resolve hint", a.Suggestion)
	}
}

// TestMaybeAutostartHubReusesHealthyHub checks an already-healthy hub is
// announced without spawning anything.
func TestMaybeAutostartHubReusesHealthyHub(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != webserver.APIPrefix+"/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(webserver.Health{Status: "ok", Version: version})
	}))
	defer ts.Close()

	host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	cfg := config.Config{ServeBind: host, ServePort: port, ServeAutostart: true}

	var buf bytes.Buffer
	maybeAutostartHub(context.Background(), cfg, false, &buf)

	if want := "Web UI: http://" + host + ":" + portStr + "/"; !strings.Contains(buf.String(), want) {
		t.Fatalf("output %q does not announce %q", buf.String(), want)
	}
}

// TestMaybeAutostartHubPortBusySkips checks a non-hub process on the port
// keeps the existing skip message and never claims a web UI.
func TestMaybeAutostartHubPortBusySkips(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()

	host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	cfg := config.Config{ServeBind: host, ServePort: port, ServeAutostart: true}

	var buf bytes.Buffer
	maybeAutostartHub(context.Background(), cfg, false, &buf)

	out := buf.String()
	if !strings.Contains(out, "port "+portStr+" is busy") {
		t.Fatalf("output %q lost the port-busy skip message", out)
	}
	if strings.Contains(out, "Web UI:") {
		t.Fatalf("output %q claims a web UI with no hub up", out)
	}
}

// TestOpenWebUIPolicy pins the Open Web UI action's decisions: a healthy hub
// opens regardless of autostart settings, a busy port and a suppressed
// autostart each name their reason instead of failing silently.
func TestOpenWebUIPolicy(t *testing.T) {
	healthHub := func(t *testing.T) (config.Config, string) {
		t.Helper()
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != webserver.APIPrefix+"/health" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(webserver.Health{Status: "ok", Version: version})
		}))
		t.Cleanup(ts.Close)
		host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
		if err != nil {
			t.Fatalf("split addr: %v", err)
		}
		port, _ := strconv.Atoi(portStr)
		return config.Config{ServeBind: host, ServePort: port}, "http://" + host + ":" + portStr + "/"
	}
	deadPort := func(t *testing.T) config.Config {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		return config.Config{ServeBind: "127.0.0.1", ServePort: port}
	}

	t.Run("healthy hub opens even with autostart off", func(t *testing.T) {
		cfg, wantURL := healthHub(t)
		a := &appActions{cfg: cfg}
		url, err := a.OpenWebUI(context.Background())
		if err != nil || url != wantURL {
			t.Fatalf("OpenWebUI = %q, %v; want %q, nil", url, err, wantURL)
		}
	})

	t.Run("busy port names the reason", func(t *testing.T) {
		ts := httptest.NewServer(http.NotFoundHandler())
		defer ts.Close()
		host, portStr, _ := net.SplitHostPort(ts.Listener.Addr().String())
		port, _ := strconv.Atoi(portStr)
		a := &appActions{cfg: config.Config{ServeBind: host, ServePort: port, ServeAutostart: true}}
		if _, err := a.OpenWebUI(context.Background()); err == nil || !strings.Contains(err.Error(), "SERVE_PORT") {
			t.Fatalf("busy-port error = %v, want SERVE_PORT hint", err)
		}
	})

	t.Run("autostart off names the reason", func(t *testing.T) {
		a := &appActions{cfg: deadPort(t)}
		if _, err := a.OpenWebUI(context.Background()); err == nil || !strings.Contains(err.Error(), "SERVE_AUTOSTART=0") {
			t.Fatalf("suppressed-autostart error = %v, want SERVE_AUTOSTART=0 reason", err)
		}
	})

	t.Run("no-serve session names the reason", func(t *testing.T) {
		cfg := deadPort(t)
		cfg.ServeAutostart = true
		a := &appActions{cfg: cfg, opts: config.Options{NoServe: true}}
		if _, err := a.OpenWebUI(context.Background()); err == nil || !strings.Contains(err.Error(), "--no-serve") {
			t.Fatalf("no-serve error = %v, want --no-serve reason", err)
		}
	})
}

// TestHubLogPathUsesTrauHome checks the hub log lives beside the hub database
// under TRAU_HOME.
func TestHubLogPathUsesTrauHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)

	if got, want := hubLogPath(), filepath.Join(home, "hub.log"); got != want {
		t.Fatalf("hubLogPath() = %q, want %q", got, want)
	}
}

// TestOpenHubLogTruncatesPerSpawn checks each spawn starts a fresh log, so the
// file only ever holds the latest boot's output.
func TestOpenHubLogTruncatesPerSpawn(t *testing.T) {
	t.Setenv("TRAU_HOME", filepath.Join(t.TempDir(), "trau"))

	first := openHubLog(os.O_TRUNC)
	if first == nil {
		t.Fatal("openHubLog returned nil for a writable home")
	}
	if _, err := first.WriteString("first boot\n"); err != nil {
		t.Fatalf("write first log: %v", err)
	}
	_ = first.Close()

	second := openHubLog(os.O_TRUNC)
	if second == nil {
		t.Fatal("openHubLog returned nil on respawn")
	}
	if _, err := second.WriteString("second boot\n"); err != nil {
		t.Fatalf("write second log: %v", err)
	}
	_ = second.Close()

	data, err := os.ReadFile(hubLogPath())
	if err != nil {
		t.Fatalf("read hub.log: %v", err)
	}
	if string(data) != "second boot\n" {
		t.Fatalf("hub.log = %q, want only the latest spawn's output", data)
	}
}

// TestOpenHubLogAppendsForRestart checks a restart-spawn keeps the outgoing
// hub's output, so a successor that dies at boot is diagnosable next to the
// reason its predecessor was replaced.
func TestOpenHubLogAppendsForRestart(t *testing.T) {
	t.Setenv("TRAU_HOME", filepath.Join(t.TempDir(), "trau"))

	old := openHubLog(os.O_TRUNC)
	if old == nil {
		t.Fatal("openHubLog returned nil for a writable home")
	}
	if _, err := old.WriteString("outgoing hub\n"); err != nil {
		t.Fatalf("write outgoing log: %v", err)
	}
	_ = old.Close()

	successor := openHubLog(os.O_APPEND)
	if successor == nil {
		t.Fatal("openHubLog returned nil on restart-spawn")
	}
	if _, err := successor.WriteString("successor hub\n"); err != nil {
		t.Fatalf("write successor log: %v", err)
	}
	_ = successor.Close()

	data, err := os.ReadFile(hubLogPath())
	if err != nil {
		t.Fatalf("read hub.log: %v", err)
	}
	if string(data) != "outgoing hub\nsuccessor hub\n" {
		t.Fatalf("hub.log = %q, want both boots", data)
	}
}

// spawnArgvRecordEnv turns a re-executed test binary into a stand-in for the
// trau binary a spawn resolves to: it records the argv it was handed and exits.
const spawnArgvRecordEnv = "TRAU_TEST_SPAWN_ARGV_RECORD"

func TestMain(m *testing.M) {
	if record := os.Getenv(spawnArgvRecordEnv); record != "" && len(os.Args) > 1 && os.Args[1] == "serve" {
		_ = os.WriteFile(record, []byte(strings.Join(os.Args[1:], " ")), 0o644)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestRespawnServeReplaysServeFlags checks the successor inherits the outgoing
// hub's serve flags, so a hub started on an explicit --port comes back on that
// port rather than on the configured default.
func TestRespawnServeReplaysServeFlags(t *testing.T) {
	record := spawnArgvRecorder(t)

	if err := respawnServe([]string{"--port", "8795", "--bind", "0.0.0.0", "--verbose"}); err != nil {
		t.Fatalf("respawnServe: %v", err)
	}

	got := waitForSpawnArgv(t, record)
	if want := "serve --port 8795 --bind 0.0.0.0 --verbose"; got != want {
		t.Fatalf("successor argv = %q, want %q", got, want)
	}
}

// TestSpawnDetachedServeTakesNoFlags checks a cold start spawns a bare serve,
// leaving the config to decide where the hub listens.
func TestSpawnDetachedServeTakesNoFlags(t *testing.T) {
	record := spawnArgvRecorder(t)

	if err := spawnDetachedServe(); err != nil {
		t.Fatalf("spawnDetachedServe: %v", err)
	}

	if got := waitForSpawnArgv(t, record); got != "serve" {
		t.Fatalf("cold-start argv = %q, want %q", got, "serve")
	}
}

func spawnArgvRecorder(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	record := filepath.Join(home, "argv")
	t.Setenv(spawnArgvRecordEnv, record)
	return record
}

func waitForSpawnArgv(t *testing.T, record string) string {
	t.Helper()
	for range 200 {
		if b, err := os.ReadFile(record); err == nil {
			return string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("spawned process never recorded its argv at %s", record)
	return ""
}
