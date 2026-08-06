package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
)

func skipsOf(t *testing.T, ts *httptest.Server, repo, id string) []string {
	t.Helper()
	res, body := getQueue(t, ts, repo)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET queue status = %d, want 200", res.StatusCode)
	}
	for _, it := range body.Items {
		if it.ID == id {
			return it.Skips
		}
	}
	t.Fatalf("%s is not in the queue view", id)
	return nil
}

// TestEnqueueStoresSkips proves the set an operator ticked off in the step picker
// rides through the enqueue into the stored item and back out of the queue view,
// in canonical order whatever order it arrived in.
func TestEnqueueStoresSkips(t *testing.T) {
	_, _, ts := queueServer(t, "acme")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{
		Kind:  "ticket",
		ID:    "COD-11",
		Skips: []string{"merge", " Verify ", "merge"},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	if got := skipsOf(t, ts, "acme", "COD-11"); !reflect.DeepEqual(got, []string{"verify", "merge"}) {
		t.Errorf("skips = %v, want [verify merge] canonicalized and deduped", got)
	}
}

// TestEnqueueRejectsUnknownSkip proves a key the loop cannot act on is refused up
// front, naming the valid set, rather than stored and silently ignored by the run.
func TestEnqueueRejectsUnknownSkip(t *testing.T) {
	_, _, ts := queueServer(t, "acme")

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{
		Kind:  "ticket",
		ID:    "COD-11",
		Skips: []string{"verify", "deploy"},
	})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(body.Error, "deploy") {
		t.Errorf("error = %q, want the offending key named", body.Error)
	}
	for _, key := range []string{"lintfix", "cleanup", "verify", "ci", "merge"} {
		if !strings.Contains(body.Error, key) {
			t.Errorf("error = %q, want the valid set to include %q", body.Error, key)
		}
	}
	res2, view := getQueue(t, ts, "acme")
	defer func() { _ = res2.Body.Close() }()
	if len(view.Items) != 0 {
		t.Errorf("items = %d, want the refused enqueue to have stored nothing", len(view.Items))
	}
}

// TestEnqueueFrontOverwritesSkips proves Run next on an item already carrying a
// set replaces it, so the picker's answer is what the child runs — including the
// re-ticked case that clears the set outright.
func TestEnqueueFrontOverwritesSkips(t *testing.T) {
	_, _, ts := queueServer(t, "acme")

	first := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{
		Kind:  "ticket",
		ID:    "COD-11",
		Skips: []string{"verify"},
	})
	_ = first.Body.Close()

	again := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{
		Kind:  "ticket",
		ID:    "COD-11",
		Skips: []string{"ci"},
		Front: true,
	})
	defer func() { _ = again.Body.Close() }()
	if again.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the front re-queue answered 200", again.StatusCode)
	}
	if got := skipsOf(t, ts, "acme", "COD-11"); !reflect.DeepEqual(got, []string{"ci"}) {
		t.Errorf("skips = %v, want the incoming set adopted", got)
	}

	cleared := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{
		Kind:  "ticket",
		ID:    "COD-11",
		Front: true,
	})
	defer func() { _ = cleared.Body.Close() }()
	if got := skipsOf(t, ts, "acme", "COD-11"); len(got) != 0 {
		t.Errorf("skips = %v, want cleared by a re-run that skips nothing", got)
	}
}

// TestEnqueueFrontOverwritesSkipsOnPausedItem proves a paused row answers the
// picker over the wire the same way a pending one does — 200 with the new set
// adopted, not the 409 that would have left the stale one to spawn.
func TestEnqueueFrontOverwritesSkipsOnPausedItem(t *testing.T) {
	s, _, root, ts := queueHub(t, "acme")

	first := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{
		Kind:  "ticket",
		ID:    "COD-11",
		Skips: []string{"verify", "merge"},
	})
	_ = first.Body.Close()
	if err := s.stores.Queue(root).Pause("COD-11", "handback"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	again := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{
		Kind:  "ticket",
		ID:    "COD-11",
		Front: true,
	})
	defer func() { _ = again.Body.Close() }()
	if again.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the paused row re-landed 200", again.StatusCode)
	}
	if got := skipsOf(t, ts, "acme", "COD-11"); len(got) != 0 {
		t.Errorf("skips = %v, want cleared by a re-run that skips nothing", got)
	}
}

// TestDrainSpawnsWithSkips proves an item's skip set rides into the spawn as
// --skip, for a ticket and an epic alike, and that an item carrying none spawns
// exactly as it did before.
func TestDrainSpawnsWithSkips(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1", Skips: []string{"verify"}},
		queue.Item{Kind: queue.KindEpic, ID: "COD-2", Skips: []string{"ci", "merge"}},
		queue.Item{Kind: queue.KindTicket, ID: "COD-3"},
	)

	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want spawn", act, err)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once", "--skip", "verify", "--drain-report", "COD-1"})

	if err := s.stores.Queue(root).Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want spawn of the epic", act, err)
	}
	assertArgs(t, fake.spawns[1].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-2", "--skip", "ci,merge", "--drain-report", "COD-2"})

	if err := s.stores.Queue(root).Finish("COD-2", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want spawn of the unskipped item", act, err)
	}
	assertArgs(t, fake.spawns[2].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-3", "--once", "--drain-report", "COD-3"})
}

// TestQueueRunItemSpawnsWithSkips proves the single-item run — the issue drawer's
// Run — launches its child on the same skip set the queued item carries.
func TestQueueRunItemSpawnsWithSkips(t *testing.T) {
	s, fake, root, ts := runOneServer(t)
	seedQueue(t, s, root, false, queue.Item{Kind: queue.KindTicket, ID: "COD-1", Skips: []string{"verify", "merge"}})

	res, _ := runQueueItem(t, ts, "acme", "COD-1")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once", "--skip", "verify,merge", "--drain-report", "COD-1"})
}
