package webserver

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
)

func heldEvents(t *testing.T, s *Server, root string) []string {
	t.Helper()
	return eventMsgs(t, s, root, event.KindQueueSpawnHeld)
}

func queueOf(t *testing.T, s *Server, root string) QueueResponse {
	t.Helper()
	view, err := s.queueView(root)
	if err != nil {
		t.Fatalf("queue view: %v", err)
	}
	return view
}

// TestDrainSpawnsBehindAQuarantinedHead replays the shipflock incident shape: a
// failed row at position 1, a settled one behind it and an unblocked pending item
// last, with the queue armed and no child running. The pending item has to start
// on the tick — the failed head is settled and holds nothing back — and the queue
// must not report a hold it does not have.
func TestDrainSpawnsBehindAQuarantinedHead(t *testing.T) {
	s, fake, root := drainServer(t, "shipflock")
	seedQueue(t, s, root, true,
		queue.Item{ID: "SHI-2", Status: queue.StatusFailed, Reason: "gave up on PR-base drift"},
		queue.Item{ID: "SHI-3", Status: queue.StatusDone},
		queue.Item{ID: "SHI-4"},
	)

	act, err := s.drain.tick(root)
	if err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want the pending item spawned behind the failed head", act, err)
	}
	if got := statusOf(t, s, root, "SHI-4"); got != queue.StatusRunning {
		t.Fatalf("SHI-4 = %q, want it running", got)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want one", len(fake.spawns))
	}
	if view := queueOf(t, s, root); view.Held {
		t.Errorf("queue held = %q, want a spawning drain to report no hold", view.HeldReason)
	}
}

// TestDrainNamesTheGateThatHoldsASpawn proves the whole point of the incident:
// every reason a draining queue starts nothing while runnable work waits reaches
// the operator, on the queue API and in the feed, instead of reading as an idle
// queue for hours.
func TestDrainNamesTheGateThatHoldsASpawn(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, s *Server, root string)
		gate  holdGate
		want  string
	}{
		{
			name:  "a pending self-reload",
			setup: func(_ *testing.T, s *Server, _ string) { s.requestSelfReload("/repos/trau") },
			gate:  holdSelfReload,
			want:  "self-reload onto trau",
		},
		{
			name:  "a loop already running in the repo",
			setup: func(_ *testing.T, s *Server, _ string) { s.drain.busyPIDs = func(string) []int { return []int{4242} } },
			gate:  holdRepoBusy,
			want:  "a loop is already running in this repo",
		},
		{
			name: "an epic mid-release with no finalize queued",
			setup: func(t *testing.T, s *Server, root string) {
				seedReleasing(t, s, root, "COD-9", state.ReleaseActive)
			},
			gate: holdRelease,
			want: "COD-9 is releasing",
		},
		{
			name: "every runnable item blocked",
			setup: func(t *testing.T, s *Server, root string) {
				if _, _, err := s.stores.Issues().Upsert(root, "jira", []hubstore.Issue{
					{Identifier: "COD-1", Title: "blocked", Status: "To Do", StatusGroup: "unstarted"},
					{Identifier: "COD-8", Title: "blocker", Status: "To Do", StatusGroup: "unstarted"},
				}); err != nil {
					t.Fatalf("seed issues: %v", err)
				}
				if err := s.stores.Issues().AddRelation(root, "COD-8", "COD-1"); err != nil {
					t.Fatalf("add relation: %v", err)
				}
			},
			gate: holdBlocked,
			want: "unresolved blocker holds back every runnable item (COD-1)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, root := drainServer(t, "acme")
			seedQueue(t, s, root, true, queue.Item{ID: "COD-1"})
			tc.setup(t, s, root)

			act, err := s.drain.tick(root)
			if err != nil || act != drainWait {
				t.Fatalf("tick = %q, %v, want the spawn held", act, err)
			}
			if len(fake.spawns) != 0 {
				t.Fatalf("spawns = %d, want none while the gate holds", len(fake.spawns))
			}
			view := queueOf(t, s, root)
			if !view.Held {
				t.Fatal("queue held = false, want the hold surfaced on the API")
			}
			if !strings.Contains(view.HeldReason, tc.want) {
				t.Errorf("held_reason = %q, want it to name %q", view.HeldReason, tc.want)
			}
			if view.HeldSince == "" {
				t.Error("held_since is empty, want the hold timed from its first tick")
			}
			msgs := heldEvents(t, s, root)
			if len(msgs) != 1 {
				t.Fatalf("spawn-held events = %d, want one per hold episode", len(msgs))
			}
			if !strings.HasPrefix(msgs[0], "spawn held: "+string(tc.gate)) {
				t.Errorf("event msg = %q, want it to name the %q gate", msgs[0], tc.gate)
			}
		})
	}
}

// TestSpawnFailureHoldsWithoutLeakingPaths keeps a failed call's own words out of
// the two places a hold is published: a spawn or store error names hub-internal
// files, and both the queue API and the feed are read by whoever is asking why
// the queue is idle.
func TestSpawnFailureHoldsWithoutLeakingPaths(t *testing.T) {
	s, fake, root := drainServer(t, "shipflock")
	seedQueue(t, s, root, true, queue.Item{ID: "SHI-4"})
	fake.spawnErr = errors.New("fork/exec /private/tmp/hub/repos/shipflock/bin/trau: no such file or directory")

	act, err := s.drain.tick(root)
	if act != drainWait || err == nil {
		t.Fatalf("tick = %q, %v, want the failed spawn held with the error still carried up", act, err)
	}
	view := queueOf(t, s, root)
	if !strings.Contains(view.HeldReason, "SHI-4") || strings.Contains(view.HeldReason, "/") {
		t.Errorf("held_reason = %q, want it to name SHI-4 without a path", view.HeldReason)
	}
	msgs := heldEvents(t, s, root)
	if len(msgs) != 1 || strings.Contains(msgs[0], "/") {
		t.Errorf("spawn-held events = %q, want one path-free line", msgs)
	}
}

// TestDrainHoldEpisodeIsAnnouncedOnce proves the feed carries the start of a wait
// rather than a line every drainPoll: a gate that holds for hours costs one
// event, a gate that gives way to a different one starts a new episode, and a
// spawn ends the hold on the API.
func TestDrainHoldEpisodeIsAnnouncedOnce(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"})
	s.drain.busyPIDs = func(string) []int { return []int{4242} }

	for range 3 {
		if act, _ := s.drain.tick(root); act != drainWait {
			t.Fatalf("tick = %q, want the busy repo to hold the spawn", act)
		}
	}
	if msgs := heldEvents(t, s, root); len(msgs) != 1 {
		t.Fatalf("spawn-held events = %d after three ticks, want one for the episode", len(msgs))
	}

	s.drain.busyPIDs = func(string) []int { return nil }
	s.requestSelfReload(root)
	if act, _ := s.drain.tick(root); act != drainWait {
		t.Fatal("want the self-reload gate to hold the spawn once the repo frees up")
	}
	msgs := heldEvents(t, s, root)
	if len(msgs) != 2 {
		t.Fatalf("spawn-held events = %d, want a second episode for the new gate", len(msgs))
	}
	if !strings.Contains(msgs[1], string(holdSelfReload)) {
		t.Errorf("second event = %q, want it to name the self-reload gate", msgs[1])
	}
	if reason := queueOf(t, s, root).HeldReason; !strings.Contains(reason, "self-reload") {
		t.Errorf("held_reason = %q, want the API to follow the gate that now holds", reason)
	}

	s.selfReloadMu.Lock()
	s.selfReload = ""
	s.selfReloadMu.Unlock()
	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatal("want the item spawned once every gate opens")
	}
	if view := queueOf(t, s, root); view.Held {
		t.Errorf("queue held = %q after the spawn, want the hold cleared", view.HeldReason)
	}
	if len(fake.spawns) != 1 {
		t.Errorf("spawns = %d, want the held item started exactly once", len(fake.spawns))
	}
}

// TestQueueReportsAnArmedQueueNothingIsDraining covers the hold no tick is left
// to announce: a queue armed with a loop that stopped ticking — one that exited
// while the flag stayed set, or one parked inside a call — reads as held rather
// than as an idle queue, which is what five silent hours looked like.
func TestQueueReportsAnArmedQueueNothingIsDraining(t *testing.T) {
	s, _, root := drainServer(t, "shipflock")
	seedQueue(t, s, root, true, queue.Item{ID: "SHI-4"})

	view := queueOf(t, s, root)
	if !view.Held {
		t.Fatal("queue held = false with no drain loop behind an armed queue, want the stall surfaced")
	}
	if !strings.Contains(view.HeldReason, "armed with nothing draining it") {
		t.Errorf("held_reason = %q, want it to say the queue is armed with no loop", view.HeldReason)
	}
	if view.HeldSince == "" {
		t.Error("held_since is empty, want a stall on an unwatched loop timed from the arm")
	}

	for range 2 {
		s.drain.announceStalls()
	}
	msgs := heldEvents(t, s, root)
	if len(msgs) != 1 {
		t.Fatalf("spawn-held events = %d, want the stall announced once for the episode", len(msgs))
	}
	if !strings.HasPrefix(msgs[0], "spawn held: "+string(holdStalled)) {
		t.Errorf("event msg = %q, want it to name the %q gate", msgs[0], holdStalled)
	}

	s.drain.busyPIDs = func(string) []int { return []int{4242} }
	if _, err := s.drain.tick(root); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if reason := queueOf(t, s, root).HeldReason; !strings.Contains(reason, "already running") {
		t.Errorf("held_reason = %q, want a ticking loop to report the gate it stopped at", reason)
	}

	s.drain.now = func() time.Time { return time.Now().Add(-2 * drainStallAfter) }
	if _, err := s.drain.tick(root); err != nil {
		t.Fatalf("stale tick: %v", err)
	}
	s.drain.now = time.Now
	if reason := queueOf(t, s, root).HeldReason; !strings.Contains(reason, "armed with nothing draining it") {
		t.Errorf("held_reason = %q, want a loop past drainStallAfter to read as stalled, not as its stale gate", reason)
	}
}

// TestQueueReportsNoHoldWhileDisarmed proves the field tracks the drain rather
// than the queue's contents: a queue nobody armed is idle, not held, however much
// runnable work it carries.
func TestQueueReportsNoHoldWhileDisarmed(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	seedQueue(t, s, root, false, queue.Item{ID: "COD-1"})

	if view := queueOf(t, s, root); view.Held || view.HeldReason != "" {
		t.Errorf("held = %v (%q), want a disarmed queue to report no hold", view.Held, view.HeldReason)
	}
}
