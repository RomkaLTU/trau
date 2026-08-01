package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/launchd"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// stopEnv isolates a stop from the machine running the test: its own trau home,
// its own HOME (so a developer's real LaunchAgent never answers for the guard),
// and no inherited TRAU_ACTIVE.
func stopEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TRAU_HOME", home)
	t.Setenv("TRAU_ACTIVE", "")
	t.Setenv("TRAU_SERVE_BIND", "127.0.0.1")
	return home
}

// fakeHub serves the routes a stop drives and points the layered config at
// itself. Its /hub/stop closes the listener the way a real drain does, so the
// command's wait for a free port is exercised rather than short-circuited.
// queueStop is the status its queue stop answers with, so a test can drive the
// observe-only repo's 403 as well as an allowlisted repo's 202, and its per-loop
// stop always fails the way a Windows hub's does — it cannot signal a loop in a
// console of its own — which is the fallback the command has to survive.
func fakeHub(t *testing.T, instances []registry.Entry, queueStop int, calls *[]string) *httptest.Server {
	t.Helper()
	var (
		mu sync.Mutex
		ts *httptest.Server
	)
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, name)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(webserver.APIPrefix+"/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(webserver.Health{Status: "ok", Version: "9.9.9", UptimeSeconds: 42})
	})
	mux.HandleFunc(webserver.APIPrefix+"/instances", func(w http.ResponseWriter, _ *http.Request) {
		live := make([]map[string]any, 0, len(instances))
		for _, e := range instances {
			live = append(live, map[string]any{"pid": e.PID, "repo_root": e.RepoRoot, "ticket": e.Ticket})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"instances": live})
	})
	mux.HandleFunc(webserver.APIPrefix+"/repos/{repo}/queue/stop", func(w http.ResponseWriter, r *http.Request) {
		record("queue/stop " + r.PathValue("repo"))
		w.WriteHeader(queueStop)
	})
	mux.HandleFunc(webserver.APIPrefix+"/instances/{pid}/stop", func(w http.ResponseWriter, r *http.Request) {
		record("instances/" + r.PathValue("pid") + "/stop")
		w.WriteHeader(http.StatusBadGateway)
	})
	mux.HandleFunc(webserver.APIPrefix+"/hub/stop", func(w http.ResponseWriter, _ *http.Request) {
		record("hub/stop")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(webserver.StopAck{Stopping: true, Version: "9.9.9"})
		go ts.Close()
	})

	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("split %s: %v", ts.URL, err)
	}
	t.Setenv("TRAU_SERVE_PORT", port)
	return ts
}

func TestStopRejectsUnknownArg(t *testing.T) {
	err := runStop(context.Background(), []string{"--nonsense"}, io.Discard, io.Discard)

	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("runStop returned %v, want a usage error", err)
	}
}

// TestStopGuards covers the refusals that have to be reached before anything is
// stopped — two of them unconditionally, --force included, because neither can
// leave the hub stopped.
func TestStopGuards(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		active    string
		supervise bool
		entry     *registry.Entry
		want      []string
	}{
		{
			name:   "inside a managed run",
			args:   []string{"--force"},
			active: "1",
			want:   []string{"trau-managed run"},
		},
		{
			name:      "launchd supervises the hub",
			args:      []string{"--force"},
			supervise: true,
			want:      []string{"trau hub unsupervise"},
		},
		{
			name:  "a live loop would be cut off",
			entry: &registry.Entry{PID: os.Getpid(), RepoRoot: filepath.FromSlash("/repos/one"), Ticket: "COD-1"},
			want:  []string{"COD-1", fmt.Sprint(os.Getpid()), "--force"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.supervise && !launchd.Supported() {
				t.Skip("launchd is macOS only")
			}
			home := stopEnv(t)
			t.Setenv("TRAU_SERVE_PORT", freePort(t))
			t.Setenv("TRAU_ACTIVE", tt.active)
			if tt.supervise {
				seedPlist(t)
			}
			if tt.entry != nil {
				seedInstance(t, home, *tt.entry)
			}

			err := runStop(context.Background(), tt.args, io.Discard, io.Discard)

			if err == nil {
				t.Fatal("runStop stopped the hub, want a refusal")
			}
			refusal := console.FormatActionable(err)
			for _, want := range tt.want {
				if !strings.Contains(refusal, want) {
					t.Errorf("refusal %q does not mention %q", refusal, want)
				}
			}
		})
	}
}

// TestStopWithNothingRunning keeps the command safe to run blind: nothing to
// stop is the outcome it was asked for, not a failure.
func TestStopWithNothingRunning(t *testing.T) {
	stopEnv(t)
	port := freePort(t)
	t.Setenv("TRAU_SERVE_PORT", port)

	var out strings.Builder
	if err := runStop(context.Background(), nil, &out, io.Discard); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if want := "no hub is running on 127.0.0.1:" + port; !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want it to say %q", out.String(), want)
	}
}

// TestStopEndsAHealthyHub covers the whole point of the command: the hub is
// asked to stop and the command returns only once the port it held is free.
func TestStopEndsAHealthyHub(t *testing.T) {
	stopEnv(t)
	var calls []string
	fakeHub(t, nil, http.StatusAccepted, &calls)

	var out strings.Builder
	if err := runStop(context.Background(), nil, &out, io.Discard); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if !strings.Contains(out.String(), "hub stopped (9.9.9)") {
		t.Errorf("output = %q, want it to name the outgoing version", out.String())
	}
	if !strings.Contains(out.String(), "is free") {
		t.Errorf("output = %q, want it to confirm the port is free", out.String())
	}
}

// TestStopForceStopsLoopsBeforeTheHub covers the ordering --force exists for: a
// loop with no hub can neither checkpoint nor reach its tracker, so every run
// goes through its queue — parked at its checkpoint, still queued — and is
// confirmed gone before the hub follows.
func TestStopForceStopsLoopsBeforeTheHub(t *testing.T) {
	stopEnv(t)
	root := t.TempDir()
	live := []registry.Entry{{PID: 1 << 22, RepoRoot: root, Ticket: "COD-1"}}
	var calls []string
	fakeHub(t, live, http.StatusAccepted, &calls)

	var out strings.Builder
	if err := runStop(context.Background(), []string{"--force"}, &out, io.Discard); err != nil {
		t.Fatalf("runStop --force: %v", err)
	}

	want := []string{"queue/stop " + filepath.Base(root), "hub/stop"}
	if !slices.Equal(calls, want) {
		t.Fatalf("hub calls = %v, want %v", calls, want)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("stopped loop COD-1 (pid %d)", live[0].PID)) {
		t.Errorf("output = %q, want it to name the stopped loop", out.String())
	}
}

// TestStopForceSurvivesAnUndeliverableLoopStop covers the observe-only repo the
// shipped SERVE_WORKSPACE default makes every repo: the queue stop is declined,
// the per-loop stop is one the hub cannot deliver, and the run still has to be
// confirmed gone and the hub still has to stop.
func TestStopForceSurvivesAnUndeliverableLoopStop(t *testing.T) {
	stopEnv(t)
	root := t.TempDir()
	live := []registry.Entry{{PID: 1 << 22, RepoRoot: root, Ticket: "COD-1"}}
	var calls []string
	fakeHub(t, live, http.StatusForbidden, &calls)

	var out strings.Builder
	if err := runStop(context.Background(), []string{"--force"}, &out, io.Discard); err != nil {
		t.Fatalf("runStop --force: %v", err)
	}

	want := []string{
		"queue/stop " + filepath.Base(root),
		fmt.Sprintf("instances/%d/stop", live[0].PID),
		"hub/stop",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("hub calls = %v, want %v", calls, want)
	}
	if !strings.Contains(out.String(), "hub stopped (9.9.9)") {
		t.Errorf("output = %q, want the hub stopped after the loop", out.String())
	}
}
