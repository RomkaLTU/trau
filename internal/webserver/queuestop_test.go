package webserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// stopServer is shutdownServer with the drain's own cadence compressed and its
// context bound to the test, since a stop leans on the drain tick to park the
// item once the child it ended is gone.
func stopServer(t *testing.T, name string) (*Server, *fakeSupervisor, string, *httptest.Server) {
	t.Helper()
	s, fake, root, ts := shutdownServer(t, name)
	s.drain.poll = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	s.drainCtx = ctx
	t.Cleanup(cancel)
	return s, fake, root, ts
}

func postStop(t *testing.T, ts *httptest.Server, repo string) (*http.Response, QueueResponse) {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/queue/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read stop body: %v", err)
	}
	var out QueueResponse
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusAccepted {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode stop body %q: %v", body, err)
		}
	}
	return res, out
}

// waitStopSettled polls the Queue view until id reads status with the stop no
// longer in flight. Both halves matter and neither implies the other: the drain
// tick can park the item before the stop goroutine has dropped the flag, so a
// snapshot taken on the first parked read can still legitimately say stopping.
func waitStopSettled(t *testing.T, ts *httptest.Server, repo, id, status string) QueueResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, q := getQueue(t, ts, repo)
		if view, found := queueViewByID(q, id); found && view.Status == status && !q.Stopping {
			return q
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never reached %s with the stop settled in the %s queue", id, status, repo)
	return QueueResponse{}
}

// TestQueueStopEndsTheRunningChildAndParksTheItem is the whole gesture: the
// drain disarms, the child that was running is stopped, and the item parks at
// its checkpoint instead of being cleared — so the ticket is resumable and the
// rest of the queue is still there for a later Start.
func TestQueueStopEndsTheRunningChildAndParksTheItem(t *testing.T) {
	s, fake, root, ts := stopServer(t, "acme")
	fake.onSignal = func(pid int, sig syscall.Signal) { _ = syscall.Kill(pid, sig) }
	repo := filepath.Base(root)

	pid := spawnSleeper(t, "5")
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: pid},
		queue.Item{ID: "COD-2"},
	)
	// What the loop's own SIGTERM handler leaves behind before it exits.
	if err := s.stores.Checkpoints().Upsert(root, "COD-1", map[string]string{
		"PHASE":          state.Building,
		"FAILURE_CLASS":  state.FailStopped,
		"FAILURE_REASON": "stopped",
	}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	res, ack := postStop(t, ts, repo)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202", res.StatusCode)
	}
	if !ack.Stopping {
		t.Error("ack does not report the stop in flight")
	}
	if ack.Draining {
		t.Error("ack still reads draining, want the drain disarmed synchronously")
	}

	q := waitStopSettled(t, ts, repo, "COD-1", queue.StatusPaused)
	if registry.Alive(pid) {
		t.Error("the running child is still alive after the stop")
	}
	if view, _ := queueViewByID(q, "COD-1"); view.Reason != "stopped" {
		t.Errorf("COD-1 reason = %q, want the stop carried through", view.Reason)
	}
	if view, _ := queueViewByID(q, "COD-2"); view.Status != queue.StatusPending {
		t.Errorf("COD-2 = %q, want it left pending for a later Start", view.Status)
	}
	if _, found, err := s.stores.Checkpoints().One(root, "COD-1"); err != nil || !found {
		t.Error("the stopped item's checkpoint went with the stop — the ticket must stay resumable")
	}
}

// TestQueueStopWithNothingRunningOnlyDisarms keeps the idle case cheap: an armed
// queue with no child in flight answers 200 and simply stops advancing.
func TestQueueStopWithNothingRunningOnlyDisarms(t *testing.T) {
	s, fake, root, ts := stopServer(t, "acme")
	repo := filepath.Base(root)
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"})

	res, q := postStop(t, ts, repo)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d, want 200", res.StatusCode)
	}
	if q.Draining || q.Stopping {
		t.Errorf("queue = draining %v stopping %v, want both clear", q.Draining, q.Stopping)
	}
	if view, _ := queueViewByID(q, "COD-1"); view.Status != queue.StatusPending {
		t.Errorf("COD-1 = %q, want the pending row untouched", view.Status)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.signals) != 0 {
		t.Errorf("signals = %v, want nothing signalled with no child in flight", fake.signals)
	}
}

// TestQueueStopTwiceStopsTheChildOnce proves the in-flight latch: a second POST
// while the stop is under way answers the same way without signalling again.
func TestQueueStopTwiceStopsTheChildOnce(t *testing.T) {
	s, fake, root, ts := stopServer(t, "acme")
	repo := filepath.Base(root)

	pid := spawnTermIgnorer(t, "5")
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: pid})

	for range 2 {
		res, _ := postStop(t, ts, repo)
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("stop status = %d, want 202", res.StatusCode)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for s.isStopping(root) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.isStopping(root) {
		t.Fatal("the stop never gave up on the surviving child")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.signals) != 1 {
		t.Errorf("signals = %d, want the child signalled once", len(fake.signals))
	}
}

// TestQueueStopEndsARunTheQueueNeverLaunched is the CLI start: nothing in the
// queue reads running, but the repo has a loop in flight — the state the Loop
// view still renders a Stop for — and the stop has to reach it.
func TestQueueStopEndsARunTheQueueNeverLaunched(t *testing.T) {
	s, fake, root, ts := stopServer(t, "acme")
	fake.onSignal = func(pid int, sig syscall.Signal) { _ = syscall.Kill(pid, sig) }
	repo := filepath.Base(root)

	pid := spawnSleeper(t, "5")
	seedQueue(t, s, root, false, queue.Item{ID: "COD-1"})
	if err := s.stores.Instances().Upsert(registry.Entry{
		PID:          pid,
		RepoRoot:     root,
		Ticket:       "COD-9",
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	res, ack := postStop(t, ts, repo)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202", res.StatusCode)
	}
	if !ack.Stopping {
		t.Error("ack does not report the stop in flight")
	}

	deadline := time.Now().Add(2 * time.Second)
	for s.isStopping(root) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if registry.Alive(pid) {
		t.Error("the CLI-started run is still alive after the stop")
	}
}

func TestQueueStopRefusedForObserveOnlyRepo(t *testing.T) {
	_, _, _, ts := stopServer(t, "acme")

	res, _ := postStop(t, ts, "elsewhere")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
}

func TestQueueStopRejectsUnsupportedMethod(t *testing.T) {
	_, _, root, ts := stopServer(t, "acme")

	res, _ := get(t, ts, APIPrefix+"/repos/"+filepath.Base(root)+"/queue/stop")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
	if allow := res.Header.Get("Allow"); allow != http.MethodPost {
		t.Errorf("Allow = %q, want POST", allow)
	}
}
