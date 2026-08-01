package webserver

import (
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
)

// pauseRunningItem drives the tick that settles the seeded running child, so a
// case starts from an item the drain itself parked rather than a hand-written
// paused row — the auto-resume plan only exists on that path.
func pauseRunningItem(t *testing.T, s *Server, root, id, class string) {
	t.Helper()
	s.drain.outcome = func(string, queue.Item) (string, string) { return class, class + " wall" }
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("settling tick = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, id); got != queue.StatusPaused {
		t.Fatalf("%s = %q after the settling tick, want paused", id, got)
	}
}

func TestAutoResumeRearmsBlamelessPauseAfterBackoff(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.autoTries = func(string) int { return 2 }
	s.drain.backoff = time.Minute
	now := time.Now()
	s.drain.now = func() time.Time { return now }
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning, PID: 7})

	pauseRunningItem(t, s, root, "COD-1", state.FailPaused)
	if drainingOf(t, s, root) {
		t.Fatal("a blameless pause disarms the queue; auto-resume re-arms it later, not immediately")
	}

	if act, _ := s.drain.tick(root); act != drainWait {
		t.Fatalf("tick inside the backoff = %q, want wait — the drain stays alive for the re-attempt", act)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawns = %d inside the backoff, want none", len(fake.spawns))
	}

	now = now.Add(2 * time.Minute)
	if act, _ := s.drain.tick(root); act != drainWait {
		t.Fatalf("tick at the due time = %q, want wait — it re-arms and the next tick spawns", act)
	}
	if !drainingOf(t, s, root) {
		t.Fatal("the queue should be armed again once the backoff passed")
	}
	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("tick after the re-arm = %q, want spawn", act)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d after the re-attempt, want 1", len(fake.spawns))
	}

	rows, err := s.stores.Events().Recent(root, 10, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var msg string
	for _, row := range rows {
		if row.Kind == event.KindQueueAutoResumed {
			msg = row.Msg
		}
	}
	if msg == "" {
		t.Fatal("no queue_auto_resumed event, want the re-attempt explained in the feed")
	}
}

// TestAutoResumeKeepsSettledWorkSettled covers a run started with the Start
// dialog's skip-resume box ticked: the re-attempt re-arms the queue where it
// stands, so an item that already shipped stays done instead of being returned to
// pending and run a second time, while the run's own no-resume flag still rides
// with the item that parked.
func TestAutoResumeKeepsSettledWorkSettled(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.autoTries = func(string) int { return 2 }
	s.drain.backoff = 0
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)
	store := s.stores.Queue(root)
	if err := store.Arm(true, queue.OnFaultHalt); err != nil {
		t.Fatalf("arm no-resume: %v", err)
	}
	if err := store.Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("finish COD-1: %v", err)
	}
	if err := store.MarkRunning("COD-2", 7); err != nil {
		t.Fatalf("mark COD-2 running: %v", err)
	}

	pauseRunningItem(t, s, root, "COD-2", state.FailPaused)
	if act, _ := s.drain.tick(root); act != drainWait {
		t.Fatalf("tick at the due time = %q, want wait — it re-arms and the next tick spawns", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusDone {
		t.Fatalf("COD-1 = %q after the re-arm, want done — shipped work must not be re-run", got)
	}

	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("tick after the re-arm = %q, want spawn", act)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1 — only the parked item is re-attempted", len(fake.spawns))
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-2", "--once", "--no-resume", "--drain-report", "COD-2"})
}

func TestAutoResumeStopsAtItsBudget(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	s.drain.autoTries = func(string) int { return 1 }
	s.drain.backoff = 0
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning, PID: 7})

	pauseRunningItem(t, s, root, "COD-1", state.FailPaused)
	if act, _ := s.drain.tick(root); act != drainWait {
		t.Fatalf("tick after the pause = %q, want wait — the one re-attempt is due", act)
	}
	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("tick after the re-arm = %q, want spawn", act)
	}

	// The re-attempt walls the same way; with the budget spent the item parks.
	if err := s.stores.Queue(root).MarkRunning("COD-1", 7); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	pauseRunningItem(t, s, root, "COD-1", state.FailPaused)
	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick after the budget = %q, want stop — the item is parked for a human", act)
	}
	if drainingOf(t, s, root) {
		t.Fatal("a spent budget must leave the queue disarmed")
	}
}

// TestAutoResumeIgnoresNonBlamelessPauses draws the policy line: only a provider
// or hub wall is re-attempted. A fault, an outcome nobody can classify, and a
// deliberate stop all park for a human however the repo is configured.
func TestAutoResumeIgnoresNonBlamelessPauses(t *testing.T) {
	for _, class := range []string{state.FailFaulted, state.FailStopped, classUnknown} {
		t.Run(class, func(t *testing.T) {
			s, _, root := drainServer(t, "acme")
			s.drain.autoTries = func(string) int { return 2 }
			s.drain.backoff = 0
			seedQueue(t, s, root, true, queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning, PID: 7})

			pauseRunningItem(t, s, root, "COD-1", class)
			if act, _ := s.drain.tick(root); act != drainStop {
				t.Fatalf("tick after a %s pause = %q, want stop — only a blameless wall is re-attempted", class, act)
			}
		})
	}
}

// TestAutoResumeLeavesAHandedOffEpicAlone: a release parked for a human is not a
// wall that clears on its own — re-attempting it would re-run a finalize whose
// only remaining move is the operator's merge. The item settles awaiting-merge
// without ever paying into an auto-resume plan, so the drain moves straight on to
// the next item and stops when the queue runs dry.
func TestAutoResumeLeavesAHandedOffEpicAlone(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.autoTries = func(string) int { return 2 }
	s.drain.backoff = 0
	s.drain.outcome = func(string, queue.Item) (string, string) {
		return state.FailAwaitingMerge, "epic COD-1 awaits a human — CI never went green: https://gh/pr/7"
	}
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindEpic, ID: "COD-1", Status: queue.StatusRunning, PID: 7})

	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("settling tick = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusAwaitingMerge {
		t.Fatalf("COD-1 = %q, want %q", got, queue.StatusAwaitingMerge)
	}
	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick after the hand-off = %q, want stop — nothing is re-armed", act)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawns = %d, want none — a human's merge is not a re-attempt", len(fake.spawns))
	}
}

func TestAutoResumeOffByDefault(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	s.drain.backoff = 0
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning, PID: 7})

	pauseRunningItem(t, s, root, "COD-1", state.FailPaused)
	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick after the pause = %q, want stop — the opt-in is off", act)
	}
}

// TestConfiguredAutoResumeTriesReadsRepoConfig covers the opt-in itself: the
// budget comes from the repo's own .trau.ini, and stays zero until it says so.
func TestConfiguredAutoResumeTriesReadsRepoConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _, root := drainServer(t, "acme")
	if got := s.drain.configuredAutoResumeTries(root); got != 0 {
		t.Fatalf("tries = %d for an unconfigured repo, want 0", got)
	}
	writeRepoINI(t, root, "QUEUE_AUTO_RESUME=1\nQUEUE_AUTO_RESUME_TRIES=3\n")
	if got := s.drain.configuredAutoResumeTries(root); got != 3 {
		t.Fatalf("tries = %d, want the repo's 3", got)
	}
}
