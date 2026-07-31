package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/proc"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// waitQueueMissing polls the Queue view until id is gone, the signal that the
// async stop-then-remove goroutine has run to completion.
func waitQueueMissing(t *testing.T, ts *httptest.Server, repo, id string) QueueResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, q := getQueue(t, ts, repo)
		if _, found := queueViewByID(q, id); !found {
			return q
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never left the %s queue", id, repo)
	return QueueResponse{}
}

func queueViewByID(q QueueResponse, id string) (QueueItemView, bool) {
	for _, it := range q.Items {
		if it.ID == id {
			return it, true
		}
	}
	return QueueItemView{}, false
}

// TestDequeueRunningWithStopStopsChildAndKeepsTicket is the whole gesture: the
// running item's child is stopped with escalation, only the queue row goes, and
// the checkpoint the stop left behind survives so the ticket is re-queueable.
func TestDequeueRunningWithStopStopsChildAndKeepsTicket(t *testing.T) {
	s, fake, root, ts := stopServer(t, "acme")
	fake.onKill = func(pid int) { _ = proc.KillGroup(pid) }
	repo := filepath.Base(root)
	store := s.stores.Queue(root)

	pid := spawnTermIgnorer(t, "5")
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add COD-1: %v", err)
	}
	if err := store.MarkRunning("COD-1", pid); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-2"}); err != nil {
		t.Fatalf("Add COD-2: %v", err)
	}
	if err := s.stores.Checkpoints().Upsert(root, "COD-1", map[string]string{"PHASE": state.Building}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/"+repo+"/queue/COD-1?stop=1")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("delete running with stop = %d, want 202 (body %q)", res.StatusCode, body)
	}
	var ack QueueResponse
	if err := json.Unmarshal([]byte(body), &ack); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view, found := queueViewByID(ack, "COD-1"); !found || !view.Removing {
		t.Errorf("acked item = %+v, want COD-1 still listed and marked removing", view)
	}

	q := waitQueueMissing(t, ts, repo, "COD-1")
	if len(q.Items) != 1 || q.Items[0].ID != "COD-2" {
		t.Errorf("queue = %+v, want only COD-2 left", q.Items)
	}
	if registry.Alive(pid) {
		t.Error("the removed item's child is still alive")
	}
	if _, found, err := s.stores.Checkpoints().One(root, "COD-1"); err != nil || !found {
		t.Error("the removed item's checkpoint went with the queue row — the ticket must be kept")
	}
}

// TestDequeueRunningWithoutStopIsStillRefused keeps the plain dequeue exactly as
// it was: a running row is a 409 that changes nothing unless the caller opts into
// the stop.
func TestDequeueRunningWithoutStopIsStillRefused(t *testing.T) {
	s, _, root, ts := stopServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.MarkRunning("COD-1", 4242); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/"+repo+"/queue/COD-1")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("delete running = %d, want 409 (body %q)", res.StatusCode, body)
	}
	if _, q := getQueue(t, ts, repo); len(q.Items) != 1 || q.Items[0].Status != queue.StatusRunning {
		t.Errorf("queue = %+v, want the running row untouched", q.Items)
	}
}

// TestDequeueRunningLeavesRowWhenChildSurvives proves the removal never orphans
// a child: a process that outlives the escalation keeps its queue row, so a
// later DELETE retries the whole sequence rather than losing track of it.
func TestDequeueRunningLeavesRowWhenChildSurvives(t *testing.T) {
	s, _, root, ts := stopServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)

	pid := spawnTermIgnorer(t, "5")
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.MarkRunning("COD-1", pid); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/"+repo+"/queue/COD-1?stop=1")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("delete running with stop = %d, want 202 (body %q)", res.StatusCode, body)
	}

	deadline := time.Now().Add(2 * time.Second)
	for s.isRemoving(root, "COD-1") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.isRemoving(root, "COD-1") {
		t.Fatal("the removal never gave up on the surviving child")
	}
	items, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(items) != 1 || items[0].Status != queue.StatusRunning {
		t.Errorf("queue = %+v, want the running row kept for a retry", items)
	}
}

// TestDequeueRunningTwiceStopsOnce proves the in-flight latch: a second DELETE
// while the removal is under way answers the same way without signalling the
// child again.
func TestDequeueRunningTwiceStopsOnce(t *testing.T) {
	s, fake, root, ts := stopServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)

	pid := spawnTermIgnorer(t, "5")
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.MarkRunning("COD-1", pid); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	for range 2 {
		res, body := deleteReq(t, ts, APIPrefix+"/repos/"+repo+"/queue/COD-1?stop=1")
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("delete running with stop = %d, want 202 (body %q)", res.StatusCode, body)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for s.isRemoving(root, "COD-1") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	fake.mu.Lock()
	stops := len(fake.stops)
	fake.mu.Unlock()
	if stops != 1 {
		t.Errorf("stops = %d, want the child signalled once", stops)
	}
}

// TestDrainWaitsOutARemoval proves the drain leaves a row whose removal is in
// flight alone: without the hold, the tick that finds the stopped child would
// park the item and disarm the queue, so removing a running item would stop the
// drain instead of advancing it.
func TestDrainWaitsOutARemoval(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	s.drain.outcome = func(string, queue.Item) (string, string) {
		return state.FailStopped, "stopped"
	}
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 4242},
		queue.Item{ID: "COD-2"},
	)
	if !s.beginRemoving(root, "COD-1") {
		t.Fatal("beginRemoving reported a removal already in flight")
	}

	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainWait {
		t.Errorf("tick = %q, want %q while the removal is in flight", act, drainWait)
	}
	if !drainingOf(t, s, root) {
		t.Error("the drain was disarmed by a row that is being removed, not paused")
	}

	s.endRemoving(root, "COD-1")
	if _, err := s.stores.Queue(root).ForceRemove("COD-1"); err != nil {
		t.Fatalf("ForceRemove: %v", err)
	}
	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Errorf("tick after the removal = (%q, %v), want the drain to spawn COD-2", act, err)
	}
}
