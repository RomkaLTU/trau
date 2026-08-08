package webserver

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/proc"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// laneServer is drainServer plus a repo that provisions worktrees, so the lane cap
// the drain reads is the repo's own WORKTREE_PARALLEL rather than the single lane
// every shared-checkout repo keeps. alive answers for the fake supervisor's PIDs,
// which is what makes a spawned child count as a lane on the next tick.
func laneServer(t *testing.T, lanes string) (*Server, *fakeSupervisor, string) {
	t.Helper()
	s, fake, root := drainServer(t, "acme")
	trees := filepath.Join(t.TempDir(), "worktrees")
	writeRepoINI(t, root, "LINEAR_TEAM=COD\nWORKTREES=1\nWORKTREES_DIR="+trees+
		"\nWORKTREE_PARALLEL="+lanes+"\n")
	live := map[int]bool{}
	s.drain.alive = func(pid int) bool { return live[pid] }
	fake.onSpawn = func(pid int) { live[pid] = true }
	return s, fake, root
}

// TestLaneCapFollowsTheRepoConfig pins the one place the cap is decided: worktrees
// off keeps the single lane every shared checkout has always had, whatever
// WORKTREE_PARALLEL says, and worktrees on takes the key at its word.
func TestLaneCapFollowsTheRepoConfig(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	writeRepoINI(t, root, "WORKTREE_PARALLEL=4\n")
	if got := s.repoLaneCap(root); got != 1 {
		t.Errorf("lane cap without worktrees = %d, want 1 — a shared checkout runs one at a time", got)
	}

	sw, _, wroot := laneServer(t, "3")
	if got := sw.repoLaneCap(wroot); got != 3 {
		t.Errorf("lane cap = %d, want 3", got)
	}
	writeRepoINI(t, wroot, "WORKTREES=1\nWORKTREE_PARALLEL=0\n")
	if got := sw.repoLaneCap(wroot); got != 1 {
		t.Errorf("lane cap for WORKTREE_PARALLEL=0 = %d, want 1", got)
	}
}

// TestDrainFillsEveryLaneThenHolds is the slice's headline: four queued tickets on
// a WORKTREES=1 repo all start, each as its own child with its own tree, and the
// fifth waits behind a lanes-full hold the queue API can be asked about.
func TestDrainFillsEveryLaneThenHolds(t *testing.T) {
	s, fake, root := laneServer(t, "4")
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1"}, queue.Item{ID: "COD-2"}, queue.Item{ID: "COD-3"},
		queue.Item{ID: "COD-4"}, queue.Item{ID: "COD-5"},
	)

	for i := 1; i <= 4; i++ {
		act, err := s.drain.tick(root)
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if act != drainSpawn {
			t.Fatalf("tick %d = %q, want %q — four lanes must all fill", i, act, drainSpawn)
		}
	}
	if len(fake.spawns) != 4 {
		t.Fatalf("spawned %d children, want 4", len(fake.spawns))
	}

	trees := map[string]bool{}
	for _, spec := range fake.spawns {
		tree := flagValue(spec.Args, "--worktree")
		if tree == "" {
			t.Fatalf("spawn %v carries no --worktree — a lane must run in its own tree", spec.Args)
		}
		if trees[tree] {
			t.Fatalf("two lanes share the worktree %s", tree)
		}
		trees[tree] = true
	}

	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("fifth tick: %v", err)
	}
	if act != drainWait {
		t.Fatalf("fifth tick = %q, want %q — the fifth item waits for a lane", act, drainWait)
	}
	if len(fake.spawns) != 4 {
		t.Fatalf("spawned %d children, want the cap to hold at 4", len(fake.spawns))
	}
	view := queueOf(t, s, root)
	if !view.Held || view.HeldGate != string(holdLanesFull) {
		t.Fatalf("held = %v gate = %q, want a lanes-full hold", view.Held, view.HeldGate)
	}
	if !strings.Contains(view.HeldReason, "4 run lanes") {
		t.Errorf("held_reason = %q, want it to name the cap", view.HeldReason)
	}
	if view.Lanes != 4 {
		t.Errorf("lanes = %d, want 4", view.Lanes)
	}
	if got := statusOf(t, s, root, "COD-5"); got != queue.StatusPending {
		t.Errorf("COD-5 = %q, want it still pending behind the full lanes", got)
	}
}

// TestSingleLaneReproducesTheSerialDrain is the regression guard for every repo
// that never opts into worktrees: one spawn per tick, the next item untouched, and
// no hold reported — a drain working through its queue is not a drain held up.
func TestSingleLaneReproducesTheSerialDrain(t *testing.T) {
	s, fake, root := laneServer(t, "1")
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"}, queue.Item{ID: "COD-2"})

	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("first tick = %q (%v), want %q", act, err, drainSpawn)
	}
	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if act != drainWait {
		t.Fatalf("second tick = %q, want %q — one lane runs one item at a time", act, drainWait)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawned %d children, want 1", len(fake.spawns))
	}
	if view := queueOf(t, s, root); view.Held {
		t.Errorf("held = %q, want a serial drain in flight to report no hold", view.HeldReason)
	}
	if got := statusOf(t, s, root, "COD-2"); got != queue.StatusPending {
		t.Errorf("COD-2 = %q, want pending behind the serial drain", got)
	}
}

// TestATerminalRunTakesALane covers the shared cap: a TUI or CLI loop registers
// presence like any child, so a two-lane repo with one terminal run has one lane
// left and no more.
func TestATerminalRunTakesALane(t *testing.T) {
	s, fake, root := laneServer(t, "2")
	s.drain.busyPIDs = s.busyInstancePIDs
	writeInstanceEntry(t, s, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		SessionState: registry.StateWorking,
		Ticket:       "COD-9",
	})
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"}, queue.Item{ID: "COD-2"})

	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("first tick = %q (%v), want %q — one lane is still free", act, err, drainSpawn)
	}
	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if act != drainWait {
		t.Fatalf("second tick = %q, want %q — the terminal run holds the other lane", act, drainWait)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawned %d children, want 1 — the hub gets one of the two lanes", len(fake.spawns))
	}
	if view := queueOf(t, s, root); view.HeldGate != string(holdLanesFull) {
		t.Errorf("held gate = %q, want %q", view.HeldGate, holdLanesFull)
	}
}

// TestLanesNeverRunTheSameTicketTwice is what keeps the per-ticket temp files
// (internal/tmpfile, keyed by ticket id) safe under concurrency: two lanes may run
// two tickets, never one ticket twice. The running row is passed over on every
// later tick however much lane capacity is free.
func TestLanesNeverRunTheSameTicketTwice(t *testing.T) {
	s, fake, root := laneServer(t, "4")
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"}, queue.Item{ID: "COD-2"})

	for i := 1; i <= 2; i++ {
		if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
			t.Fatalf("tick %d = %q (%v), want %q", i, act, err, drainSpawn)
		}
	}
	// Two more ticks with two lanes still free and nothing left but the two rows
	// already running.
	for i := 0; i < 2; i++ {
		if act, err := s.drain.tick(root); err != nil || act != drainWait {
			t.Fatalf("idle tick = %q (%v), want %q", act, err, drainWait)
		}
	}
	if len(fake.spawns) != 2 {
		t.Fatalf("spawned %d children, want exactly one per ticket", len(fake.spawns))
	}
	started := map[string]int{}
	for _, spec := range fake.spawns {
		started[flagValue(spec.Args, "--parent")]++
	}
	for id, n := range started {
		if n != 1 {
			t.Errorf("%s launched %d times, want 1 — a ticket's temp files are keyed by its id", id, n)
		}
	}
	if len(started) != 2 {
		t.Errorf("launched %d distinct tickets, want 2", len(started))
	}
}

// sharedLaneServer is a repo without worktrees whose lane cap is widened through
// the seam the drain reads it by. A shared checkout really gets one lane, which is
// what makes its epic holds invisible from the outside — the holds themselves are
// the invariant these tests pin, and they must survive any later widening.
func sharedLaneServer(t *testing.T, lanes int) (*Server, *fakeSupervisor, string) {
	t.Helper()
	s, fake, root := drainServer(t, "acme")
	s.drain.laneCap = func(string) int { return lanes }
	live := map[int]bool{}
	s.drain.alive = func(pid int) bool { return live[pid] }
	fake.onSpawn = func(pid int) { live[pid] = true }
	return s, fake, root
}

// TestALiveEpicHoldsEveryLaneOnASharedCheckout is the shared-checkout invariant: an
// epic drives several tickets through one checkout, so nothing else may run in the
// repo while it does. Only a repo whose runs each get their own worktree lifts it.
func TestALiveEpicHoldsEveryLaneOnASharedCheckout(t *testing.T) {
	s, fake, root := sharedLaneServer(t, 4)
	seedQueue(t, s, root, true,
		queue.Item{Kind: queue.KindEpic, ID: "COD-9"}, queue.Item{ID: "COD-1"})

	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("first tick = %q (%v), want the epic to start", act, err)
	}
	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if act != drainWait {
		t.Fatalf("second tick = %q, want %q — a live epic holds the repo", act, drainWait)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawned %d children, want 1 while the epic runs", len(fake.spawns))
	}
	if reason := queueOf(t, s, root).HeldReason; !strings.Contains(reason, "epic holds the whole repo") {
		t.Errorf("held_reason = %q, want it to name the epic hold", reason)
	}
}

// TestAQueuedEpicWaitsForEveryLaneOnASharedCheckout is the same rule from the other
// side: an epic next in run order does not start beside the ticket lanes already in
// flight in the checkout it would have to share with them.
func TestAQueuedEpicWaitsForEveryLaneOnASharedCheckout(t *testing.T) {
	s, fake, root := sharedLaneServer(t, 4)
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 4242},
		queue.Item{Kind: queue.KindEpic, ID: "COD-9"},
	)
	s.drain.alive = func(pid int) bool { return pid == 4242 }

	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainWait {
		t.Fatalf("tick = %q, want %q — an epic waits for the lanes to drain", act, drainWait)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawned %d children, want none", len(fake.spawns))
	}
	if reason := queueOf(t, s, root).HeldReason; !strings.Contains(reason, "runs on its own") {
		t.Errorf("held_reason = %q, want it to say the epic runs on its own", reason)
	}
}

// TestQueueStopEndsEveryLane pins the Stop the header offers once a repo drains
// more than one ticket: it disarms the drain and ends every child in flight, not
// only the first row that reads running.
func TestQueueStopEndsEveryLane(t *testing.T) {
	s, fake, root, ts := stopServer(t, "acme")
	fake.onStop = func(pid int) { _ = proc.StopGracefully(pid) }
	repo := filepath.Base(root)

	first, second := spawnSleeper(t, "5"), spawnSleeper(t, "5")
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: first},
		queue.Item{ID: "COD-2", Status: queue.StatusRunning, PID: second},
		queue.Item{ID: "COD-3"},
	)

	res, ack := postStop(t, ts, repo)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202", res.StatusCode)
	}
	if ack.Draining {
		t.Error("ack still reads draining, want the drain disarmed synchronously")
	}
	waitStopSettled(t, ts, repo, "COD-1", queue.StatusPaused)
	waitStopSettled(t, ts, repo, "COD-2", queue.StatusPaused)
	if registry.Alive(first) || registry.Alive(second) {
		t.Error("a lane survived the stop — every child in flight has to be ended")
	}
}

// flagValue reads the value a spawn's argv gives a flag, or "" when the flag is
// absent — enough to tell one lane's launch from another's.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
