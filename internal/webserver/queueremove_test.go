package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
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

// TestDequeueRunningWithStopEjectsTheWholeRun is the whole gesture: the running
// item's child is stopped with escalation, the row goes, and the run behind it is
// wiped — checkpoint dropped and the ticket reset back to Ready — so a later
// pickup starts fresh rather than resuming this attempt.
func TestDequeueRunningWithStopEjectsTheWholeRun(t *testing.T) {
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
	assertReset(t, fake, root, "COD-1")
	if _, found, err := s.stores.Checkpoints().One(root, "COD-1"); err != nil || found {
		t.Error("the removed item kept its checkpoint — a later pickup would resume the run it was ejected from")
	}
	assertRemovedEvent(t, s, root, "COD-1")
}

// assertReset waits for the removal's reset children and fails unless they are
// exactly one per id, in order.
func assertReset(t *testing.T, fake *fakeSupervisor, root string, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		fake.mu.Lock()
		captures := append([]SpawnSpec(nil), fake.captures...)
		fake.mu.Unlock()
		if len(captures) >= len(ids) || time.Now().After(deadline) {
			if len(captures) != len(ids) {
				t.Fatalf("reset children = %+v, want one per %v", captures, ids)
			}
			for i, id := range ids {
				assertArgs(t, captures[i].Args, []string{"--repo", root, "--reset", id, "--no-tui"})
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// assertRemovedEvent polls for the removal event, which lands after the row
// leaves the queue and so is not covered by waiting the row out.
func assertRemovedEvent(t *testing.T, s *Server, root, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err := s.stores.Events().Recent(root, 10, 0)
		if err != nil {
			t.Fatalf("recent events: %v", err)
		}
		for _, row := range rows {
			if row.Kind == event.KindQueueItemRemoved && strings.Contains(row.Msg, id) {
				if !strings.Contains(row.Msg, "back to Ready") {
					t.Errorf("removal event = %q, want it to name the ticket going back to Ready", row.Msg)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("events = %+v, want a %s for %s", rows, event.KindQueueItemRemoved, id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDequeueUnrunRowEjectsWithoutAStop covers every row a removal needs no stop
// for: each one wipes its run and readies its ticket exactly as the running row
// does, and none of them signals a child.
func TestDequeueUnrunRowEjectsWithoutAStop(t *testing.T) {
	for _, status := range []string{queue.StatusPending, queue.StatusPaused, queue.StatusFailed} {
		t.Run(status, func(t *testing.T) {
			s, fake, root, ts := stopServer(t, "acme")
			repo := filepath.Base(root)
			seedQueue(t, s, root, false, queue.Item{ID: "COD-1", Status: status})
			seedRunPhase(t, s, root, "COD-1", state.Verified)

			res, body := deleteReq(t, ts, APIPrefix+"/repos/"+repo+"/queue/COD-1")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("delete %s = %d, want 200 (body %q)", status, res.StatusCode, body)
			}
			assertReset(t, fake, root, "COD-1")
			waitCheckpointGone(t, s, root, "COD-1")
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.stops) > 0 || len(fake.kills) > 0 {
				t.Errorf("stops = %v, kills = %v, want the row removed without a stop", fake.stops, fake.kills)
			}
		})
	}
}

// TestWipeLeavesAShippedRunAlone is the one carve-out: a row whose checkpoint says
// merged has already shipped, so removing it drops the row and nothing else —
// resetting it would take down the merged branch and re-open the ticket.
func TestWipeLeavesAShippedRunAlone(t *testing.T) {
	s, fake, root, _ := stopServer(t, "acme")
	seedRunPhase(t, s, root, "COD-1", state.Merged)

	s.wipeRemovedRun(root, queue.Item{Kind: queue.KindTicket, ID: "COD-1"})

	if _, found, err := s.stores.Checkpoints().One(root, "COD-1"); err != nil || !found {
		t.Error("the shipped run's checkpoint went with the queue row")
	}
	if len(fake.captures) > 0 {
		t.Errorf("reset children = %+v, want a merged run left alone", fake.captures)
	}
}

// TestWipeEpicCoversOnlyTheInFlightChild proves the epic rule: killing the epic
// wipes the sub-issue its child was in the middle of, while the children it
// already settled keep the state that records what they did.
func TestWipeEpicCoversOnlyTheInFlightChild(t *testing.T) {
	s, fake, root, _ := stopServer(t, "acme")
	seedRunPhase(t, s, root, "COD-10", state.Merged)
	seedRunPhase(t, s, root, "COD-11", state.HandedOff)

	s.wipeRemovedRun(root, queue.Item{
		Kind: queue.KindEpic,
		ID:   "COD-9",
		SubIssues: []queue.SubIssue{
			{ID: "COD-10", State: subIssueDone},
			{ID: "COD-11", State: "todo"},
		},
	})

	assertReset(t, fake, root, "COD-9", "COD-11")
	if _, found, err := s.stores.Checkpoints().One(root, "COD-10"); err != nil || !found {
		t.Error("the epic's shipped sub-issue lost its checkpoint")
	}
	if _, found, err := s.stores.Checkpoints().One(root, "COD-11"); err != nil || found {
		t.Error("the sub-issue the epic was running kept its checkpoint")
	}
}

// TestWipeKeepsTheBranchWhileTheRepoIsLive guards the working tree: a reset checks
// the base branch out, so a repo whose own loop is still running gets the
// checkpoint dropped and nothing else.
func TestWipeKeepsTheBranchWhileTheRepoIsLive(t *testing.T) {
	s, fake, root, _ := stopServer(t, "acme")
	s.drain.repoLive = func(string) bool { return true }
	seedRunPhase(t, s, root, "COD-1", state.Built)

	s.wipeRemovedRun(root, queue.Item{Kind: queue.KindTicket, ID: "COD-1"})

	if len(fake.captures) > 0 {
		t.Errorf("reset children = %+v, want none while a loop holds the repo", fake.captures)
	}
	if _, found, err := s.stores.Checkpoints().One(root, "COD-1"); err != nil || found {
		t.Error("the removed row kept its checkpoint")
	}
}

func seedRunPhase(t *testing.T, s *Server, root, id, phase string) {
	t.Helper()
	if err := s.stores.Checkpoints().Upsert(root, id, map[string]string{"PHASE": phase}); err != nil {
		t.Fatalf("seed %s checkpoint: %v", id, err)
	}
}

// waitCheckpointGone polls until id's checkpoint is dropped, the last step of the
// wipe the dequeue response does not wait for.
func waitCheckpointGone(t *testing.T, s *Server, root, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, found, err := s.stores.Checkpoints().One(root, id); err == nil && !found {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s kept its checkpoint — a later pickup would resume the run it was ejected from", id)
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
// a child: a process that outlives the escalation keeps its queue row and the run
// behind it untouched, so a later DELETE retries the whole sequence rather than
// wiping a run that is still going.
func TestDequeueRunningLeavesRowWhenChildSurvives(t *testing.T) {
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
	seedRunPhase(t, s, root, "COD-1", state.Building)

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
	if _, found, err := s.stores.Checkpoints().One(root, "COD-1"); err != nil || !found {
		t.Error("the surviving child's run lost its checkpoint")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.captures) > 0 {
		t.Errorf("reset children = %+v, want none while the child is still alive", fake.captures)
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
