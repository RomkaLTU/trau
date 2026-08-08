package webserver

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
)

func queueViewByID(q QueueResponse, id string) (QueueItemView, bool) {
	for _, it := range q.Items {
		if it.ID == id {
			return it, true
		}
	}
	return QueueItemView{}, false
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
	s.drain.busyPIDs = func(string) []int { return []int{4242} }
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

// TestDequeueRunningIsRefused is the whole running-row contract: removal is two
// deliberate steps, so the row is a 409 that points at the stop and changes
// nothing. The retired stop=1 opt-in is covered with it — the query is now an
// ordinary DELETE that stops no child and drops no row.
func TestDequeueRunningIsRefused(t *testing.T) {
	for _, path := range []string{"/queue/COD-1", "/queue/COD-1?stop=1"} {
		t.Run(path, func(t *testing.T) {
			s, fake, root, ts := stopServer(t, "acme")
			repo := filepath.Base(root)
			store := s.stores.Queue(root)
			if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if err := store.MarkRunning("COD-1", 4242); err != nil {
				t.Fatalf("MarkRunning: %v", err)
			}
			seedRunPhase(t, s, root, "COD-1", state.Building)

			res, body := deleteReq(t, ts, APIPrefix+"/repos/"+repo+path)
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("delete running = %d, want 409 (body %q)", res.StatusCode, body)
			}
			if !strings.Contains(body, "COD-1 is running — stop the run first, then remove it") {
				t.Errorf("refusal = %q, want it to name the stop as the first step", body)
			}
			if _, q := getQueue(t, ts, repo); len(q.Items) != 1 || q.Items[0].Status != queue.StatusRunning {
				t.Errorf("queue = %+v, want the running row untouched", q.Items)
			}
			if _, found, err := s.stores.Checkpoints().One(root, "COD-1"); err != nil || !found {
				t.Error("the refused removal wiped the run's checkpoint")
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.stops) > 0 || len(fake.kills) > 0 || len(fake.captures) > 0 {
				t.Errorf("stops = %v, kills = %v, resets = %+v, want the refusal to touch nothing",
					fake.stops, fake.kills, fake.captures)
			}
		})
	}
}
