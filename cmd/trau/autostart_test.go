package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
