package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// reloadServer builds a hub allowlisting one repo, running from a binary inside
// it, with the reload watcher compressed so an idle gap is observable in a test.
// selfReload defaults on for the repo; a case that wants the fail-closed path
// writes its own .trau.ini first.
func reloadServer(t *testing.T, selfReload bool) (*Server, *httptest.Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "acme")
	writeReloadConfig(t, root, selfReload)

	s := New("2.1.0", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	s.reloadPoll = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	s.drainCtx = ctx
	t.Cleanup(cancel)
	s.sup = &fakeSupervisor{}
	s.executable = func() (string, error) { return filepath.Join(root, "bin", "trau"), nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, root
}

func writeReloadConfig(t *testing.T, root string, selfReload bool) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	body := "HUB_SELF_RELOAD=0\n"
	if selfReload {
		body = "HUB_SELF_RELOAD=1\n"
	}
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte(body), 0o644); err != nil {
		t.Fatalf("write repo config: %v", err)
	}
}

func postReload(t *testing.T, ts *httptest.Server, root string) *http.Response {
	t.Helper()
	body, err := json.Marshal(ReloadRequest{RepoRoot: root})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	res, err := ts.Client().Post(ts.URL+APIPrefix+"/hub/reload", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST reload: %v", err)
	}
	return res
}

// TestReloadAcceptsAndDefers checks an opted-in repo gets a 202 that says the
// reload is pending rather than done, and that the hub records which repo asked.
func TestReloadAcceptsAndDefers(t *testing.T) {
	s, ts, root := reloadServer(t, true)
	s.EnableRestart(func() {})

	res := postReload(t, ts, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
	}
	var ack ReloadAck
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !ack.Pending || ack.RepoRoot != root || ack.Version != "2.1.0" {
		t.Fatalf("ack = %+v, want pending for %s at the running version", ack, root)
	}
	if got := s.selfReloadPending(); got != root {
		t.Fatalf("pending repo = %q, want %q", got, root)
	}
}

// TestReloadRefusedWhenKeyUnset pins the fail-closed rule: the hub re-reads the
// repo's own config and refuses when it never opted in, whatever the caller says.
func TestReloadRefusedWhenKeyUnset(t *testing.T) {
	s, ts, root := reloadServer(t, false)
	s.EnableRestart(func() { t.Error("a refused reload restarted the hub") })

	res := postReload(t, ts, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusForbidden)
	}
	if got := s.selfReloadPending(); got != "" {
		t.Fatalf("pending repo = %q, want nothing pending", got)
	}
}

// TestReloadRefusedWhenHubRunsElsewhere is the cask guard: a hub running from
// outside the repo would restart onto a binary that is not the one the repo
// built, so it refuses and marks nothing.
func TestReloadRefusedWhenHubRunsElsewhere(t *testing.T) {
	s, ts, root := reloadServer(t, true)
	s.EnableRestart(func() { t.Error("a refused reload restarted the hub") })
	s.executable = func() (string, error) { return "/opt/homebrew/Cellar/trau/2.1.0/bin/trau", nil }

	res := postReload(t, ts, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
	}
	if got := s.selfReloadPending(); got != "" {
		t.Fatalf("pending repo = %q, want nothing pending", got)
	}
}

// TestReloadRepeatsCoalesce checks repeated requests mark one pending reload and
// end in one restart — a merge that asks twice must not queue two successors.
func TestReloadRepeatsCoalesce(t *testing.T) {
	s, ts, root := reloadServer(t, true)
	restarts := make(chan struct{}, 4)
	s.EnableRestart(func() { restarts <- struct{}{} })

	for range 3 {
		res := postReload(t, ts, root)
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
		}
		_ = res.Body.Close()
	}

	select {
	case <-restarts:
	case <-time.After(2 * time.Second):
		t.Fatal("the hub never restarted on an idle hub")
	}
	select {
	case <-restarts:
		t.Fatal("coalesced requests restarted the hub more than once")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestReloadWaitsForIdleGap checks the reload holds while a queue child runs in
// any repo and lands the moment the hub goes quiet.
func TestReloadWaitsForIdleGap(t *testing.T) {
	s, ts, root := reloadServer(t, true)
	restarted := make(chan struct{})
	s.EnableRestart(func() { close(restarted) })
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: os.Getpid()})

	res := postReload(t, ts, root)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
	}

	select {
	case <-restarted:
		t.Fatal("the hub restarted while a queue child was still running")
	case <-time.After(50 * time.Millisecond):
	}

	if err := s.stores.Queue(root).Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("finish item: %v", err)
	}
	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("the hub never restarted once the queue went idle")
	}
}

// TestHubIdleIgnoresIdleInstances checks an open dashboard — an idle instance —
// never holds a reload back, while a working loop in any repo does.
func TestHubIdleIgnoresIdleInstances(t *testing.T) {
	cases := []struct {
		name  string
		state string
		idle  bool
	}{
		{name: "idle dashboard", state: registry.StateIdle, idle: true},
		{name: "working loop", state: registry.StateWorking, idle: false},
		{name: "takeover terminal", state: registry.StateTakeover, idle: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := reloadServer(t, true)
			writeInstanceEntry(t, s, registry.Entry{
				PID:          os.Getpid(),
				RepoRoot:     filepath.Join(t.TempDir(), "elsewhere"),
				SessionState: tc.state,
			})
			if got := s.hubIdle(); got != tc.idle {
				t.Errorf("hubIdle = %v, want %v", got, tc.idle)
			}
		})
	}
}

// TestTickHoldsSpawnWhileReloadPending checks the drain stops handing out new
// children while a reload waits: without the hold the queue re-spawns within one
// poll and the idle gap never arrives. The drain stays armed, so the successor
// picks the held item up.
func TestTickHoldsSpawnWhileReloadPending(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.requestSelfReload(root)
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"})

	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainWait {
		t.Fatalf("tick = %q, want %q — a pending reload must hold the spawn", act, drainWait)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawned %d children, want 0 while a reload is pending", len(fake.spawns))
	}
	if !drainingOf(t, s, root) {
		t.Error("drain disarmed — the held item must still be there after the restart")
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPending {
		t.Errorf("item status = %q, want %q", got, queue.StatusPending)
	}
}

// TestReloadRejectsGET keeps the endpoint out of reach of a link or a prefetch.
func TestReloadRejectsGET(t *testing.T) {
	s, ts, _ := reloadServer(t, true)
	s.EnableRestart(func() { t.Error("GET triggered a reload") })

	res, err := http.Get(ts.URL + APIPrefix + "/hub/reload")
	if err != nil {
		t.Fatalf("GET reload: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusMethodNotAllowed)
	}
}

// TestReloadRequiresTokenOnExposedBind checks the reload endpoint sits behind the
// same bearer-token auth as every other control endpoint.
func TestReloadRequiresTokenOnExposedBind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "acme")
	writeReloadConfig(t, root, true)
	s := New("2.1.0", "0.0.0.0", "s3cret", []string{root}, false, testStores(t))
	s.EnableRestart(func() { t.Error("unauthenticated POST triggered a reload") })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res := postReload(t, ts, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}
}

// TestUpdateStatusCarriesPendingReload checks the web surface can see a pending
// reload: it rides the update resource the updates section already polls.
func TestUpdateStatusCarriesPendingReload(t *testing.T) {
	s, ts, root := reloadServer(t, true)
	s.EnableRestart(func() {})
	writeInstanceEntry(t, s, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		SessionState: registry.StateWorking,
	})

	res := postReload(t, ts, root)
	_ = res.Body.Close()

	got, err := http.Get(ts.URL + APIPrefix + "/update")
	if err != nil {
		t.Fatalf("GET update: %v", err)
	}
	defer func() { _ = got.Body.Close() }()
	var payload struct {
		Running           string `json:"running"`
		SelfReloadPending string `json:"selfReloadPending"`
	}
	if err := json.NewDecoder(got.Body).Decode(&payload); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if payload.Running != "2.1.0" || payload.SelfReloadPending != root {
		t.Fatalf("update = %+v, want the running version and %s pending", payload, root)
	}
}
