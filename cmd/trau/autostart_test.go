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

	first := openHubLog()
	if first == nil {
		t.Fatal("openHubLog returned nil for a writable home")
	}
	if _, err := first.WriteString("first boot\n"); err != nil {
		t.Fatalf("write first log: %v", err)
	}
	_ = first.Close()

	second := openHubLog()
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
