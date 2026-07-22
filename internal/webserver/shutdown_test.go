package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// shutdownServer builds a server with one Registered repo, a fake supervisor,
// and the stop-and-wait timings compressed, so a shutdown test never sleeps for
// real seconds.
func shutdownServer(t *testing.T, name string) (*Server, *fakeSupervisor, string, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), name)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	s.home = home
	fake := &fakeSupervisor{}
	s.sup = fake

	prevPoll, prevConfirm := stopWaitPoll, stopKillConfirm
	stopWaitPoll, stopKillConfirm = 5*time.Millisecond, time.Second
	t.Cleanup(func() { stopWaitPoll, stopKillConfirm = prevPoll, prevConfirm })

	prevKill, prevRace := shutdownKillGrace, shutdownRaceWindow
	shutdownKillGrace, shutdownRaceWindow = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { shutdownKillGrace, shutdownRaceWindow = prevKill, prevRace })

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, fake, root, ts
}

func postShutdown(t *testing.T, ts *httptest.Server, repo string) *http.Response {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/queue/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST shutdown: %v", err)
	}
	return res
}

// waitQueueEmpty polls the Queue view until it reads empty and idle, the signal
// that the async teardown goroutine has run to completion.
func waitQueueEmpty(t *testing.T, ts *httptest.Server, repo string) QueueResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, q := getQueue(t, ts, repo)
		if len(q.Items) == 0 && !q.ShuttingDown {
			return q
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue for %s never emptied", repo)
	return QueueResponse{}
}

func TestQueueShutdownStopsRunningChildDropsPausedCheckpointsAndClears(t *testing.T) {
	s, fake, root, ts := shutdownServer(t, "acme")
	fake.onKill = func(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
	repo := filepath.Base(root)
	store := s.stores.Queue(root)

	runningPID := spawnTermIgnorer(t, "5")
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add COD-1: %v", err)
	}
	if err := store.MarkRunning("COD-1", runningPID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-2"}); err != nil {
		t.Fatalf("Add COD-2: %v", err)
	}
	if err := store.Pause("COD-2", "faulted"); err != nil {
		t.Fatalf("Pause COD-2: %v", err)
	}
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-3"}); err != nil {
		t.Fatalf("Add COD-3: %v", err)
	}
	if err := s.stores.Checkpoints().Upsert(root, "COD-1", map[string]string{"PHASE": "building"}); err != nil {
		t.Fatalf("seed checkpoint COD-1: %v", err)
	}
	if err := s.stores.Checkpoints().Upsert(root, "COD-2", map[string]string{"PHASE": "building"}); err != nil {
		t.Fatalf("seed checkpoint COD-2: %v", err)
	}

	res := postShutdown(t, ts, repo)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("shutdown status = %d, want 202", res.StatusCode)
	}
	var ack map[string]string
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack["status"] != "shutting_down" {
		t.Fatalf("ack = %v, want shutting_down", ack)
	}

	q := waitQueueEmpty(t, ts, repo)
	if q.Draining {
		t.Error("queue still draining after shutdown")
	}

	if registry.Alive(runningPID) {
		t.Error("running child still alive after shutdown")
	}
	if _, found, _ := s.stores.Checkpoints().One(root, "COD-1"); found {
		t.Error("running ticket's checkpoint survived shutdown")
	}
	if _, found, _ := s.stores.Checkpoints().One(root, "COD-2"); found {
		t.Error("paused ticket's checkpoint survived shutdown")
	}
}

func TestQueueShutdownIdleRepoJustEmptiesQueue(t *testing.T) {
	s, _, root, ts := shutdownServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	res := postShutdown(t, ts, repo)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}

	q := waitQueueEmpty(t, ts, repo)
	if len(q.Items) != 0 {
		t.Fatalf("items = %v, want none", q.Items)
	}
}

func TestQueueShutdownStopsDrainerRacedChild(t *testing.T) {
	s, fake, root, ts := shutdownServer(t, "acme")
	fake.onKill = func(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }
	repo := filepath.Base(root)
	store := s.stores.Queue(root)
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	prevRace := shutdownRaceWindow
	shutdownRaceWindow = 200 * time.Millisecond
	t.Cleanup(func() { shutdownRaceWindow = prevRace })

	racedPID := spawnTermIgnorer(t, "5")
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = s.stores.Instances().Upsert(registry.Entry{
			PID:          racedPID,
			RepoRoot:     root,
			StartedAt:    time.Now(),
			Heartbeat:    time.Now(),
			SessionState: registry.StateIdle,
		})
	}()

	res := postShutdown(t, ts, repo)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", res.StatusCode)
	}

	waitQueueEmpty(t, ts, repo)

	if registry.Alive(racedPID) {
		t.Error("drainer-raced child still alive after shutdown")
	}
}

func TestQueueShutdownSecondPOSTWhileInFlightIsNoOp(t *testing.T) {
	s, fake, root, ts := shutdownServer(t, "acme")
	repo := filepath.Base(root)
	store := s.stores.Queue(root)

	runningPID := spawnSleeper(t, "0.05")
	if _, err := store.Add(queue.Item{Kind: queue.KindTicket, ID: "COD-1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.MarkRunning("COD-1", runningPID); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	res1 := postShutdown(t, ts, repo)
	defer func() { _ = res1.Body.Close() }()
	if res1.StatusCode != http.StatusAccepted {
		t.Fatalf("first shutdown status = %d, want 202", res1.StatusCode)
	}
	res2 := postShutdown(t, ts, repo)
	defer func() { _ = res2.Body.Close() }()
	if res2.StatusCode != http.StatusAccepted {
		t.Fatalf("second shutdown status = %d, want 202", res2.StatusCode)
	}

	waitQueueEmpty(t, ts, repo)

	fake.mu.Lock()
	signals := append([]signalCall{}, fake.signals...)
	fake.mu.Unlock()
	if len(signals) != 1 {
		t.Fatalf("signals = %+v, want exactly one SIGTERM despite two POSTs", signals)
	}
}

func TestQueueShutdownRefusedForObserveOnlyRepo(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	res := postShutdown(t, ts, "stranger")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an observe-only repo", res.StatusCode)
	}
}

func TestQueueShutdownRejectsUnsupportedMethod(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	req, err := http.NewRequest(http.MethodGet, ts.URL+APIPrefix+"/repos/acme/queue/shutdown", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET shutdown: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}

func TestBeginShutdownIsIdempotentPerRepo(t *testing.T) {
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStores(t))
	root := "/tmp/acme"

	if !s.beginShutdown(root) {
		t.Fatal("first beginShutdown = false, want true")
	}
	if s.beginShutdown(root) {
		t.Fatal("second beginShutdown while in flight = true, want false")
	}
	s.endShutdown(root)
	if !s.beginShutdown(root) {
		t.Fatal("beginShutdown after endShutdown = false, want true")
	}
}
