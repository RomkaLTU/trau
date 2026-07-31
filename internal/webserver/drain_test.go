package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// drainServer builds a server whose allowlist holds one Registered repo, with a
// fake supervisor and deterministic drain probes so a tick's decision is a pure
// function of the seeded queue rather than of real processes. Tests override the
// probes per case.
func drainServer(t *testing.T, name string) (*Server, *fakeSupervisor, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), name)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	// Start arms the daily release check against GitHub; nothing here is about
	// that, and netguard would kill the binary the moment it fired.
	s.SetUpdateChecks(false)
	fake := &fakeSupervisor{}
	s.sup = fake
	s.drain.repoLive = func(string) bool { return false }
	s.drain.alive = func(int) bool { return false }
	s.drain.outcome = func(string, queue.Item) (string, string) { return "", "" }
	s.drain.prState = func(string, string) string { return "" }
	s.drain.autoTries = func(string) int { return 0 }
	return s, fake, root
}

// seedQueue writes the queue through the store's own API so a case can stage
// items already running or finished, then sets the draining flag. It uses the
// server's own queue store so the drainer and the test read the same database.
func seedQueue(t *testing.T, s *Server, root string, draining bool, items ...queue.Item) {
	t.Helper()
	st := s.stores.Queue(root)
	for _, it := range items {
		base := queue.Item{Kind: it.Kind, ID: it.ID, Title: it.Title, Source: it.Source, Provider: it.Provider, SubIssues: it.SubIssues}
		if base.Kind == "" {
			base.Kind = queue.KindTicket
		}
		if _, err := st.Add(base); err != nil {
			t.Fatalf("seed add %s: %v", it.ID, err)
		}
		switch it.Status {
		case queue.StatusRunning:
			if err := st.MarkRunning(it.ID, it.PID); err != nil {
				t.Fatalf("seed running %s: %v", it.ID, err)
			}
		case queue.StatusPaused:
			if err := st.Pause(it.ID, it.Reason); err != nil {
				t.Fatalf("seed paused %s: %v", it.ID, err)
			}
		case queue.StatusDone, queue.StatusFailed:
			if err := st.Finish(it.ID, it.Status, it.Reason); err != nil {
				t.Fatalf("seed finish %s: %v", it.ID, err)
			}
		case queue.StatusSkipped:
			if err := st.MarkSkipped(it.ID, it.Reason); err != nil {
				t.Fatalf("seed skipped %s: %v", it.ID, err)
			}
		}
	}
	if err := st.SetDraining(draining); err != nil {
		t.Fatalf("seed draining: %v", err)
	}
}

func snapshot(t *testing.T, s *Server, root string) []queue.Item {
	t.Helper()
	items, _, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return items
}

func statusOf(t *testing.T, s *Server, root, id string) string {
	t.Helper()
	for _, it := range snapshot(t, s, root) {
		if it.ID == id {
			return it.Status
		}
	}
	t.Fatalf("item %s missing from queue", id)
	return ""
}

func reasonOf(t *testing.T, s *Server, root, id string) string {
	t.Helper()
	for _, it := range snapshot(t, s, root) {
		if it.ID == id {
			return it.Reason
		}
	}
	t.Fatalf("item %s missing from queue", id)
	return ""
}

func drainingOf(t *testing.T, s *Server, root string) bool {
	t.Helper()
	_, meta, err := s.stores.Queue(root).Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return meta.Draining
}

func countStatus(t *testing.T, s *Server, root, status string) int {
	t.Helper()
	n := 0
	for _, it := range snapshot(t, s, root) {
		if it.Status == status {
			n++
		}
	}
	return n
}

func runningItem(t *testing.T, s *Server, root string) (queue.Item, bool) {
	for _, it := range snapshot(t, s, root) {
		if it.Status == queue.StatusRunning {
			return it, true
		}
	}
	return queue.Item{}, false
}

// TestDrainTickDecisions table-drives one tick over staged queue states: it
// covers spawning the next pending item, waiting on a live child, settling a
// finished one, the three failure classes (give-up settles failed and drains on;
// fault and provider pause park the item and stop the drain), the single-child
// guarantee, waiting on an external live run, pausing, and finishing the drain
// when the queue runs dry.
func TestDrainTickDecisions(t *testing.T) {
	tests := []struct {
		name          string
		items         []queue.Item
		draining      bool
		alive         map[int]bool
		repoLive      bool
		outcomeClass  string
		outcomeReason string
		report        *queue.DrainReport
		wantAction    drainAction
		wantSpawns    int
		wantStatus    map[string]string
		wantReason    map[string]string
		wantDraining  *bool
	}{
		{
			name:       "spawns the first pending item",
			items:      []queue.Item{{ID: "COD-1"}, {ID: "COD-2"}},
			draining:   true,
			wantAction: drainSpawn,
			wantSpawns: 1,
			wantStatus: map[string]string{"COD-1": queue.StatusRunning, "COD-2": queue.StatusPending},
		},
		{
			name:       "re-attempts a paused item ahead of a pending one",
			items:      []queue.Item{{ID: "COD-1", Status: queue.StatusPaused, Reason: "was faulted"}, {ID: "COD-2"}},
			draining:   true,
			wantAction: drainSpawn,
			wantSpawns: 1,
			wantStatus: map[string]string{"COD-1": queue.StatusRunning, "COD-2": queue.StatusPending},
			wantReason: map[string]string{"COD-1": ""},
		},
		{
			name:       "waits while the child is alive",
			items:      []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}},
			draining:   true,
			alive:      map[int]bool{7: true},
			wantAction: drainWait,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusRunning},
		},
		{
			name:       "settles a finished child to done on a clean report",
			items:      []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}},
			draining:   true,
			report:     &queue.DrainReport{},
			wantAction: drainReconcile,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusDone},
		},
		{
			name:         "a dead child with no drain report pauses the drain",
			items:        []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}, {ID: "COD-2"}},
			draining:     true,
			wantAction:   drainReconcile,
			wantSpawns:   0,
			wantStatus:   map[string]string{"COD-1": queue.StatusPaused, "COD-2": queue.StatusPending},
			wantReason:   map[string]string{"COD-1": "child exited without a drain report — outcome unknown"},
			wantDraining: boolPtr(false),
		},
		{
			name:          "give-up settles failed and keeps draining",
			items:         []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}},
			draining:      true,
			outcomeClass:  state.FailGaveUp,
			outcomeReason: "verify never went green",
			wantAction:    drainReconcile,
			wantSpawns:    0,
			wantStatus:    map[string]string{"COD-1": queue.StatusFailed},
			wantReason:    map[string]string{"COD-1": "verify never went green"},
			wantDraining:  boolPtr(true),
		},
		{
			name:          "fault pauses the queue and parks the item",
			items:         []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}, {ID: "COD-2"}},
			draining:      true,
			outcomeClass:  state.FailFaulted,
			outcomeReason: "unexpected error during handoff",
			wantAction:    drainReconcile,
			wantSpawns:    0,
			wantStatus:    map[string]string{"COD-1": queue.StatusPaused, "COD-2": queue.StatusPending},
			wantReason:    map[string]string{"COD-1": "unexpected error during handoff"},
			wantDraining:  boolPtr(false),
		},
		{
			name:          "provider pause stops the queue with its reason",
			items:         []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}},
			draining:      true,
			outcomeClass:  state.FailPaused,
			outcomeReason: "claude authentication required — re-login",
			wantAction:    drainReconcile,
			wantSpawns:    0,
			wantStatus:    map[string]string{"COD-1": queue.StatusPaused},
			wantReason:    map[string]string{"COD-1": "claude authentication required — re-login"},
			wantDraining:  boolPtr(false),
		},
		{
			name:       "never spawns a second child while one runs",
			items:      []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}, {ID: "COD-2"}},
			draining:   true,
			alive:      map[int]bool{7: true},
			wantAction: drainWait,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusRunning, "COD-2": queue.StatusPending},
		},
		{
			name:       "waits for an external live run instead of spawning",
			items:      []queue.Item{{ID: "COD-1"}},
			draining:   true,
			repoLive:   true,
			wantAction: drainWait,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusPending},
		},
		{
			name:       "stops when paused with nothing in flight",
			items:      []queue.Item{{ID: "COD-1"}},
			draining:   false,
			wantAction: drainStop,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusPending},
		},
		{
			name:       "settles the in-flight child even when paused",
			items:      []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}},
			draining:   false,
			report:     &queue.DrainReport{},
			wantAction: drainReconcile,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusDone},
		},
		{
			name:         "finishes the drain when the queue runs dry",
			items:        []queue.Item{{ID: "COD-1", Status: queue.StatusDone}},
			draining:     true,
			wantAction:   drainStop,
			wantSpawns:   0,
			wantStatus:   map[string]string{"COD-1": queue.StatusDone},
			wantDraining: boolPtr(false),
		},
		{
			name:         "runs dry even while an external run is live",
			items:        []queue.Item{{ID: "COD-1", Status: queue.StatusDone}},
			draining:     true,
			repoLive:     true,
			wantAction:   drainStop,
			wantSpawns:   0,
			wantStatus:   map[string]string{"COD-1": queue.StatusDone},
			wantDraining: boolPtr(false),
		},
		{
			name:         "an armed empty queue disarms itself",
			draining:     true,
			wantAction:   drainStop,
			wantSpawns:   0,
			wantDraining: boolPtr(false),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, root := drainServer(t, "acme")
			s.drain.repoLive = func(string) bool { return tc.repoLive }
			s.drain.alive = func(pid int) bool { return tc.alive[pid] }
			if tc.outcomeClass != "" {
				s.drain.outcome = func(string, queue.Item) (string, string) {
					return tc.outcomeClass, tc.outcomeReason
				}
			}
			seedQueue(t, s, root, tc.draining, tc.items...)
			if tc.report != nil {
				if it, ok := runningItem(t, s, root); ok {
					seedOutcome(t, s, root, it.ID, *tc.report)
				}
			}

			act, err := s.drain.tick(root)
			if err != nil {
				t.Fatalf("tick: %v", err)
			}
			if act != tc.wantAction {
				t.Errorf("action = %q, want %q", act, tc.wantAction)
			}
			if len(fake.spawns) != tc.wantSpawns {
				t.Errorf("spawns = %d, want %d", len(fake.spawns), tc.wantSpawns)
			}
			for id, want := range tc.wantStatus {
				if got := statusOf(t, s, root, id); got != want {
					t.Errorf("%s status = %q, want %q", id, got, want)
				}
			}
			for id, want := range tc.wantReason {
				if got := reasonOf(t, s, root, id); got != want {
					t.Errorf("%s reason = %q, want %q", id, got, want)
				}
			}
			if tc.wantDraining != nil {
				if got := drainingOf(t, s, root); got != *tc.wantDraining {
					t.Errorf("draining = %v, want %v", got, *tc.wantDraining)
				}
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// TestClassifyDrainOutcome table-drives the outcome-class → queue-action mapping
// for every class the loop records: a clean finish and a give-up drain on (done /
// failed), while a fault and a provider pause park the item and stop the drain.
func TestClassifyDrainOutcome(t *testing.T) {
	tests := []struct {
		name       string
		class      string
		onFault    string
		wantStatus string
		wantPause  bool
	}{
		{name: "clean finish settles done", class: "", wantStatus: queue.StatusDone, wantPause: false},
		{name: "unknown outcome parks regardless of on-fault", class: classUnknown, onFault: queue.OnFaultSkip, wantStatus: queue.StatusPaused, wantPause: true},
		{name: "give-up settles failed and drains on", class: state.FailGaveUp, wantStatus: queue.StatusFailed, wantPause: false},
		{name: "fault pauses the queue by default", class: state.FailFaulted, onFault: queue.OnFaultHalt, wantStatus: queue.StatusPaused, wantPause: true},
		{name: "fault skips on on-fault=skip", class: state.FailFaulted, onFault: queue.OnFaultSkip, wantStatus: queue.StatusFailed, wantPause: false},
		{name: "provider pause parks regardless of on-fault", class: state.FailPaused, onFault: queue.OnFaultSkip, wantStatus: queue.StatusPaused, wantPause: true},
		{name: "deliberate stop parks regardless of on-fault", class: state.FailStopped, onFault: queue.OnFaultSkip, wantStatus: queue.StatusPaused, wantPause: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, pause := classifyDrainOutcome(tc.class, tc.onFault)
			if status != tc.wantStatus || pause != tc.wantPause {
				t.Errorf("classifyDrainOutcome(%q, %q) = (%q, %v), want (%q, %v)", tc.class, tc.onFault, status, pause, tc.wantStatus, tc.wantPause)
			}
		})
	}
}

// TestCheckpointOutcomeReadsRecordedState proves the outcome is read from the
// run's recorded checkpoint — its phase and the loop's own failure marker/reason
// — and never from agent output.
func TestCheckpointOutcomeReadsRecordedState(t *testing.T) {
	tests := []struct {
		name       string
		phase      string
		failClass  string
		reason     string
		wantClass  string
		wantReason string
	}{
		{name: "merged is a clean finish", phase: state.Merged, wantClass: "", wantReason: ""},
		{name: "quarantine reads as give-up", phase: state.Quarantined, reason: "verify never went green", wantClass: state.FailGaveUp, wantReason: "verify never went green"},
		{name: "fault marker reads as fault", phase: state.HandedOff, failClass: state.FailFaulted, reason: "unexpected error during handoff", wantClass: state.FailFaulted, wantReason: "unexpected error during handoff"},
		{name: "pause marker reads as provider pause", phase: state.Building, failClass: state.FailPaused, reason: "claude authentication required", wantClass: state.FailPaused, wantReason: "claude authentication required"},
		{name: "stop marker reads as a deliberate stop", phase: state.Building, failClass: state.FailStopped, reason: "stopped during build — work saved at the last checkpoint", wantClass: state.FailStopped, wantReason: "stopped during build — work saved at the last checkpoint"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, root := drainServer(t, "acme")
			data := map[string]string{"PHASE": tc.phase}
			if tc.failClass != "" {
				data["FAILURE_CLASS"] = tc.failClass
			}
			if tc.reason != "" {
				data["FAILURE_REASON"] = tc.reason
			}
			if err := s.stores.Checkpoints().Upsert(root, "COD-1", data); err != nil {
				t.Fatalf("seed checkpoint: %v", err)
			}
			class, reason := s.drain.checkpointOutcome(root, queue.Item{ID: "COD-1"})
			if class != tc.wantClass || reason != tc.wantReason {
				t.Errorf("checkpointOutcome = (%q, %q), want (%q, %q)", class, reason, tc.wantClass, tc.wantReason)
			}
		})
	}
}

// TestDrainPauseAndResumeReattemptsItem faults an in-flight child: the queue
// pauses with the item parked and its reason surfaced, stays stopped until a
// resume, then re-attempts that same item before the one behind it.
func TestDrainPauseAndResumeReattemptsItem(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	class, reason := state.FailFaulted, "unexpected error during handoff"
	s.drain.outcome = func(string, queue.Item) (string, string) { return class, reason }
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 7},
		queue.Item{ID: "COD-2"},
	)

	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("settle tick = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPaused {
		t.Fatalf("COD-1 = %q, want paused", got)
	}
	if got := reasonOf(t, s, root, "COD-1"); got != reason {
		t.Errorf("COD-1 reason = %q, want the fault reason", got)
	}
	if drainingOf(t, s, root) {
		t.Error("queue still draining after a fault, want it paused")
	}

	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick while paused = %q, want stop", act)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawns = %d while paused, want none", len(fake.spawns))
	}

	class = ""
	if err := s.stores.Queue(root).SetDraining(true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("resume tick = %q, want it to re-attempt the paused item", act)
	}
	running, ok := runningItem(t, s, root)
	if !ok || running.ID != "COD-1" {
		t.Fatalf("re-attempted item = %+v, want COD-1", running)
	}
	if running.Reason != "" {
		t.Errorf("re-attempted COD-1 reason = %q, want it cleared", running.Reason)
	}
	if got := statusOf(t, s, root, "COD-2"); got != queue.StatusPending {
		t.Errorf("COD-2 = %q, want it still pending behind COD-1", got)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once", "--drain-report", "COD-1"})
}

// TestDrainRunsSequentially drives a full drain of three items to completion,
// asserting they spawn in queue order and that exactly one child is ever running
// at a time.
func TestDrainRunsSequentially(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	alive := map[int]bool{}
	s.drain.alive = func(pid int) bool { return alive[pid] }
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1"},
		queue.Item{ID: "COD-2"},
		queue.Item{Kind: queue.KindEpic, ID: "COD-3"},
	)

	var order []string
	for step := 0; step < 30 && countStatus(t, s, root, queue.StatusDone) < 3; step++ {
		act, err := s.drain.tick(root)
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		switch act {
		case drainSpawn:
			it, ok := runningItem(t, s, root)
			if !ok {
				t.Fatal("spawn reported but nothing is running")
			}
			order = append(order, it.ID)
			if n := countStatus(t, s, root, queue.StatusRunning); n != 1 {
				t.Fatalf("running items = %d after a spawn, want exactly 1", n)
			}
			alive[it.PID] = true
		case drainWait:
			if it, ok := runningItem(t, s, root); ok {
				alive[it.PID] = false
				seedOutcome(t, s, root, it.ID, queue.DrainReport{})
			}
		case drainStop:
			t.Fatal("drain stopped before finishing the queue")
		}
	}

	want := []string{"COD-1", "COD-2", "COD-3"}
	if len(order) != len(want) {
		t.Fatalf("spawn order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("spawn order = %v, want %v", order, want)
		}
	}
	if len(fake.spawns) != 3 {
		t.Errorf("spawns = %d, want one per item", len(fake.spawns))
	}
	if done := countStatus(t, s, root, queue.StatusDone); done != 3 {
		t.Errorf("done = %d, want all three settled", done)
	}
	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick after the queue ran dry = %q, want stop", act)
	}
	if drainingOf(t, s, root) {
		t.Error("draining still set after the queue ran dry, want the drain finished")
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once", "--drain-report", "COD-1"})
	assertArgs(t, fake.spawns[2].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-3", "--drain-report", "COD-3"})
}

// seedOutcome records a ticket's exit outcome through the hub store, the way a
// queued child posts it, so a drain tick reconciles from the same database it
// reads.
func seedOutcome(t *testing.T, s *Server, root, ticket string, rep queue.DrainReport) {
	t.Helper()
	if err := s.stores.DrainOutcomes().Upsert(root, ticket, rep.Class, rep.Reason); err != nil {
		t.Fatalf("seed drain outcome: %v", err)
	}
}

// TestDrainRunsBlockerBeforeBlockedItem proves the drain's blocker gate on the
// shape that inverted in production: COD-1 blocks COD-2, and COD-2 sits ahead of
// it in the queue. The drain passes COD-2 over, ships COD-1, and only then starts
// COD-2 — never the other way round.
func TestDrainRunsBlockerBeforeBlockedItem(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true,
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
	)
	if _, _, err := s.stores.Issues().Upsert(root, "jira", []hubstore.Issue{
		{Identifier: "COD-1", Title: "blocker", Status: "To Do", StatusGroup: "unstarted"},
		{Identifier: "COD-2", Title: "blocked", Status: "To Do", StatusGroup: "unstarted"},
	}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	if err := s.stores.Issues().AddRelation(root, "COD-1", "COD-2"); err != nil {
		t.Fatalf("add relation: %v", err)
	}

	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want a spawn", act, err)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusRunning {
		t.Fatalf("COD-1 = %q, want the blocker running first", got)
	}
	if got := statusOf(t, s, root, "COD-2"); got != queue.StatusPending {
		t.Fatalf("COD-2 = %q, want the blocked item left pending", got)
	}

	seedOutcome(t, s, root, "COD-1", queue.DrainReport{})
	if act, err := s.drain.tick(root); err != nil || act != drainReconcile {
		t.Fatalf("tick = %q, %v, want the finished blocker settled", act, err)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusDone {
		t.Fatalf("COD-1 = %q, want it settled done", got)
	}
	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want COD-2 spawned once its blocker shipped", act, err)
	}
	if got := statusOf(t, s, root, "COD-2"); got != queue.StatusRunning {
		t.Errorf("COD-2 = %q, want it running after the blocker", got)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once", "--drain-report", "COD-1"})
	assertArgs(t, fake.spawns[1].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-2", "--once", "--drain-report", "COD-2"})
}

// TestDrainSpawnsInternalTicket proves a hub-only internal item drains like any
// other: the spawn follows the item's kind, and its source never gates the run.
func TestDrainSpawnsInternalTicket(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindTicket, ID: "ACME-1", Source: "internal"})

	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainSpawn {
		t.Fatalf("act = %q, want spawn — an internal ticket runs like a tracker one", act)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(fake.spawns))
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "ACME-1", "--once", "--drain-report", "ACME-1"})
	if got := statusOf(t, s, root, "ACME-1"); got != queue.StatusRunning {
		t.Errorf("ACME-1 = %q, want running", got)
	}
}

// TestDrainSpawnsWithProviderOverride proves an item's Provider override rides
// into the spawn as --provider, for a ticket and an epic alike.
func TestDrainSpawnsWithProviderOverride(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true,
		queue.Item{Kind: queue.KindTicket, ID: "COD-1", Provider: "codex"},
		queue.Item{Kind: queue.KindEpic, ID: "COD-2", Provider: "kimi"},
	)

	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want spawn", act, err)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once", "--provider", "codex", "--drain-report", "COD-1"})

	if err := s.stores.Queue(root).Finish("COD-1", queue.StatusDone, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
		t.Fatalf("tick = %q, %v, want spawn of the epic", act, err)
	}
	assertArgs(t, fake.spawns[1].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-2", "--provider", "kimi", "--drain-report", "COD-2"})
}

// TestDrainSkipsDuplicateTicket proves a standalone ticket an earlier queued
// epic already covers is skipped, not run — first occurrence wins.
func TestDrainSkipsDuplicateTicket(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true,
		queue.Item{Kind: queue.KindEpic, ID: "COD-1", Status: queue.StatusDone, SubIssues: []queue.SubIssue{{ID: "COD-2"}}},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)
	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainReconcile {
		t.Fatalf("act = %q, want reconcile — a duplicate is skipped, not spawned", act)
	}
	if got := statusOf(t, s, root, "COD-2"); got != queue.StatusSkipped {
		t.Errorf("COD-2 = %q, want skipped as a duplicate", got)
	}
	if reasonOf(t, s, root, "COD-2") == "" {
		t.Error("skipped COD-2 missing a duplicate reason")
	}
	if len(fake.spawns) != 0 {
		t.Errorf("spawns = %d, want none — the duplicate must not run", len(fake.spawns))
	}
}

// A Task carrying sub-issues queues as its own epic even though the grandparent
// epic ahead of it lists it as a sub-issue. Only a standalone ticket dedups, so
// the Task still runs — draining its own children instead of once, as a ticket
// would.
func TestDrainKeepsQueuedEpicCoveredByAnEarlierEpic(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true,
		queue.Item{Kind: queue.KindEpic, ID: "PROJ-1", Status: queue.StatusDone, SubIssues: []queue.SubIssue{{ID: "PROJ-11"}}},
		queue.Item{Kind: queue.KindEpic, ID: "PROJ-11", SubIssues: []queue.SubIssue{{ID: "PROJ-12"}}},
	)
	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainSpawn {
		t.Fatalf("act = %q, want spawn — an epic is never deduped away", act)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(fake.spawns))
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "PROJ-11", "--drain-report", "PROJ-11"})
	if got := statusOf(t, s, root, "PROJ-11"); got != queue.StatusRunning {
		t.Errorf("PROJ-11 = %q, want running", got)
	}
}

// TestDrainCleansUpReportOnReconcile proves a finished child's drain report is
// consumed and removed when the drain reconciles it to a clean finish.
func TestDrainCleansUpReportOnReconcile(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 4242})
	seedOutcome(t, s, root, "COD-1", queue.DrainReport{})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("act = %q, want reconcile of the finished child", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusDone {
		t.Errorf("COD-1 = %q, want done", got)
	}
	if _, found, _ := s.stores.DrainOutcomes().One(root, "COD-1"); found {
		t.Error("drain outcome not cleaned up after reconcile")
	}
}

// TestDrainReportFaultParksEpic proves a fault the child reports parks the item
// even when its own checkpoint reads clean — the case of an epic whose fault
// lives on a sub-issue.
func TestDrainReportFaultParksEpic(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	s.drain.outcome = func(string, queue.Item) (string, string) { return "", "" }
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindEpic, ID: "COD-1", Status: queue.StatusRunning, PID: 7})
	seedOutcome(t, s, root, "COD-1", queue.DrainReport{Class: state.FailFaulted, Reason: "sub-issue faulted"})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("act = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPaused {
		t.Errorf("COD-1 = %q, want paused — the child reported a fault the epic checkpoint hides", got)
	}
	if drainingOf(t, s, root) {
		t.Error("draining still set after a fault park")
	}
}

// TestDrainReportUnfinalizedEpicPausesThenShips is the COD-1127 acceptance: an
// epic child whose finalize declined while a sibling still read open posts a
// pause, so the item parks with the waiting-on reason instead of settling done
// with the epic branch unmerged and the Loop page disagreeing with the tracker. A
// start re-attempts that same item, and the re-run that does ship the epic
// settles it done.
func TestDrainReportUnfinalizedEpicPausesThenShips(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedQueue(t, s, root, true, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-1",
		Status:    queue.StatusRunning,
		PID:       7,
		SubIssues: []queue.SubIssue{{ID: "COD-2", State: "backlog"}},
	})
	reason := "epic COD-1 unfinalized — waiting on COD-2"
	seedOutcome(t, s, root, "COD-1", queue.DrainReport{Class: state.FailPaused, Reason: reason})

	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("settle tick = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPaused {
		t.Fatalf("COD-1 = %q, want paused — a declined finalize must not settle done", got)
	}
	if got := reasonOf(t, s, root, "COD-1"); got != reason {
		t.Errorf("COD-1 reason = %q, want the waiting-on reason %q", got, reason)
	}
	if drainingOf(t, s, root) {
		t.Error("draining still set after parking an unfinalized epic")
	}

	if err := s.stores.Queue(root).SetDraining(true); err != nil {
		t.Fatalf("start: %v", err)
	}
	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("start tick = %q, want the paused epic re-attempted", act)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--drain-report", "COD-1"})

	seedOutcome(t, s, root, "COD-1", queue.DrainReport{})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("re-run tick = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusDone {
		t.Errorf("COD-1 = %q, want done once the re-run shipped the epic", got)
	}
}

// TestDrainNoReportPausesEpicWithoutFanout is the COD-813 acceptance: an epic
// child killed mid-run leaves no drain report, and an epic that never shipped
// wrote no merged checkpoint, so the drain has zero evidence of a clean finish.
// It must park the epic — halting the drain for a human — with an explanatory
// reason, and never settle it done, which would stamp every carried sub-issue
// done.
func TestDrainNoReportPausesEpicWithoutFanout(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	seedQueue(t, s, root, true, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-1",
		Status:    queue.StatusRunning,
		PID:       7,
		SubIssues: []queue.SubIssue{{ID: "COD-2", State: "backlog"}, {ID: "COD-3", State: "backlog"}},
	})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("act = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPaused {
		t.Errorf("COD-1 = %q, want paused — a dead epic child with no report must not settle done", got)
	}
	if reasonOf(t, s, root, "COD-1") == "" {
		t.Error("paused COD-1 missing the outcome-unknown reason")
	}
	if drainingOf(t, s, root) {
		t.Error("draining still set after parking an unknown outcome")
	}
	for _, it := range snapshot(t, s, root) {
		if it.ID != "COD-1" {
			continue
		}
		for _, sub := range it.SubIssues {
			if sub.State != "backlog" {
				t.Errorf("sub %s state = %q, want its enqueue-time backlog — a park must not fan out", sub.ID, sub.State)
			}
		}
	}
}

// TestDrainNoReportMergedTicketSettlesDone proves the clean-finish safety valve:
// a ticket whose report was lost still settles done when its own checkpoint
// proves it reached merged — positive evidence the fix accepts in the report's
// absence, so a lost report never re-pauses an already-merged ticket.
func TestDrainNoReportMergedTicketSettlesDone(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	if err := s.stores.Checkpoints().Upsert(root, "COD-1", map[string]string{"PHASE": state.Merged}); err != nil {
		t.Fatalf("seed merged checkpoint: %v", err)
	}
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning, PID: 7})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("act = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusDone {
		t.Errorf("COD-1 = %q, want done — a merged checkpoint is clean-finish evidence even with the report lost", got)
	}
	if !drainingOf(t, s, root) {
		t.Error("draining cleared on a clean finish, want the drain to keep going")
	}
}

// TestDrainHonorsConfiguredRunsDir descends from the COD-811 regression: a repo
// whose cwd-local trau.ini sets a non-default RUNS_DIR must still resolve that
// dir for its drain report (repoRunsDir), not a hardcoded .trau/runs. Checkpoints
// now live in the authoritative table (dir-independent), so a fault recorded
// there must park a faulted epic regardless of the runs dir.
func TestDrainHonorsConfiguredRunsDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TRAU_ENV", "")
	s, _, root := drainServer(t, "acme")
	s.drain.outcome = s.drain.checkpointOutcome
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "trau.ini"), []byte("RUNS_DIR=runs\n"), 0o644); err != nil {
		t.Fatalf("write trau.ini: %v", err)
	}
	runsDir := repoRunsDir(root)
	if want := filepath.Join(root, "runs"); runsDir != want {
		t.Fatalf("repoRunsDir = %q, want %q from the configured RUNS_DIR", runsDir, want)
	}
	if err := s.stores.Checkpoints().Upsert(root, "COD-1", map[string]string{
		"PHASE":          state.HandedOff,
		"FAILURE_CLASS":  state.FailFaulted,
		"FAILURE_REASON": "context canceled",
	}); err != nil {
		t.Fatalf("seed fault checkpoint: %v", err)
	}
	seedQueue(t, s, root, true, queue.Item{Kind: queue.KindEpic, ID: "COD-1", Status: queue.StatusRunning, PID: 7})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("act = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPaused {
		t.Errorf("COD-1 = %q, want paused — a fault recorded under the configured RUNS_DIR must not settle done", got)
	}
	if got := reasonOf(t, s, root, "COD-1"); got != "context canceled" {
		t.Errorf("COD-1 reason = %q, want the fault reason surfaced", got)
	}
	if drainingOf(t, s, root) {
		t.Error("draining still set after a fault park")
	}
}

// TestDrainOnFaultSkipContinues proves on-fault=skip settles the faulted item
// failed and keeps draining instead of parking the queue.
func TestDrainOnFaultSkipContinues(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	seedQueue(t, s, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 7},
		queue.Item{ID: "COD-2"},
	)
	if err := s.stores.Queue(root).Arm(false, queue.OnFaultSkip); err != nil {
		t.Fatalf("arm on-fault skip: %v", err)
	}
	seedOutcome(t, s, root, "COD-1", queue.DrainReport{Class: state.FailFaulted, Reason: "boom"})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("act = %q, want reconcile", act)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusFailed {
		t.Errorf("COD-1 = %q, want failed and skipped past on on-fault=skip", got)
	}
	if !drainingOf(t, s, root) {
		t.Error("draining cleared on on-fault=skip, want the drain to keep going")
	}
}

// TestDrainPauseTakesEffectAfterCurrentChild pauses while a child is in flight:
// the running item still settles, the queue then stops, and the next item is
// left pending for a later start.
func TestDrainPauseTakesEffectAfterCurrentChild(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	alive := map[int]bool{}
	s.drain.alive = func(pid int) bool { return alive[pid] }
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"}, queue.Item{ID: "COD-2"})

	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("first tick = %q, want spawn", act)
	}
	running, _ := runningItem(t, s, root)
	alive[running.PID] = true

	if err := s.stores.Queue(root).SetDraining(false); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if act, _ := s.drain.tick(root); act != drainWait {
		t.Fatalf("tick while the child runs = %q, want wait (no mid-run kill)", act)
	}
	if statusOf(t, s, root, "COD-1") != queue.StatusRunning {
		t.Error("COD-1 must keep running until its child exits")
	}

	alive[running.PID] = false
	seedOutcome(t, s, root, running.ID, queue.DrainReport{})
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("tick after the child exits = %q, want reconcile", act)
	}
	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick once settled = %q, want stop (pause took effect)", act)
	}
	if statusOf(t, s, root, "COD-1") != queue.StatusDone {
		t.Error("COD-1 should be settled done")
	}
	if statusOf(t, s, root, "COD-2") != queue.StatusPending {
		t.Error("COD-2 should stay pending for a later start")
	}
}

// TestDrainResumeSettlesLeftoverRunning is the restart case: a hub comes up with
// an item persisted as running whose child already exited cleanly, its drain
// outcome still recorded in the store. The resume settles the leftover done from
// that outcome and the queue continues.
func TestDrainResumeSettlesLeftoverRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(t.TempDir(), "acme")
	first := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	first.home = home
	first.sup = &fakeSupervisor{}
	seedQueue(t, first, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 999999},
		queue.Item{ID: "COD-2"},
	)

	second := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStoresAt(t, home))
	second.home = home
	second.sup = &fakeSupervisor{}
	second.drain.alive = func(int) bool { return false }
	second.drain.repoLive = func(string) bool { return false }

	if _, running := firstWithStatus(snapshot(t, second, root), queue.StatusRunning); !running {
		t.Fatal("precondition: COD-1 should be persisted as running")
	}
	seedOutcome(t, second, root, "COD-1", queue.DrainReport{})
	if act, _ := second.drain.tick(root); act != drainReconcile {
		t.Fatalf("first resumed tick = %q, want it to settle the leftover run", act)
	}
	if statusOf(t, second, root, "COD-1") != queue.StatusDone {
		t.Errorf("leftover COD-1 = %q, want settled done", statusOf(t, second, root, "COD-1"))
	}
	if act, _ := second.drain.tick(root); act != drainSpawn {
		t.Fatalf("next tick = %q, want it to continue with COD-2", act)
	}
	if statusOf(t, second, root, "COD-2") != queue.StatusRunning {
		t.Error("COD-2 should now be running")
	}
}

func TestDrainEndpointStartsAndPauses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false, testStores(t))
	s.home = t.TempDir()
	s.sup = &fakeSupervisor{}
	// A live run holds the repo, so the armed loop waits instead of spawning and
	// the endpoint's own writes are what the assertions read.
	s.drain.repoLive = func(string) bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.drainCtx = ctx
	seedQueue(t, s, root, false, queue.Item{ID: "COD-1"})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/drain", DrainRequest{Draining: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start = %d, want 200", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Draining {
		t.Error("response draining = false, want true after start")
	}
	if out.DrainingSince == "" {
		t.Error("response carries no draining_since, want the stamp the Loop page times the run from")
	}
	if _, meta, _ := s.stores.Queue(root).Snapshot(); !meta.Draining {
		t.Error("draining flag not persisted after start")
	}
	s.drain.mu.Lock()
	_, active := s.drain.active[root]
	s.drain.mu.Unlock()
	if !active {
		t.Error("start did not launch a drain loop for the repo")
	}

	pause := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/drain", DrainRequest{Draining: false})
	defer func() { _ = pause.Body.Close() }()
	var paused QueueResponse
	if err := json.NewDecoder(pause.Body).Decode(&paused); err != nil {
		t.Fatalf("decode pause: %v", err)
	}
	if paused.Draining {
		t.Error("response draining = true, want false after pause")
	}
	if paused.DrainingSince != "" {
		t.Errorf("response draining_since = %q, want it dropped with the drain", paused.DrainingSince)
	}
	if _, meta, _ := s.stores.Queue(root).Snapshot(); meta.Draining {
		t.Error("draining flag not cleared after pause")
	}
}

// A queue with nothing pending or paused has no run to start, so the endpoint
// refuses it 409 and leaves the metadata, the items and the drain loop alone
// rather than arming over work that does not exist.
func TestDrainEndpointRefusesStartWithNothingRunnable(t *testing.T) {
	tests := []struct {
		name  string
		items []queue.Item
	}{
		{name: "empty queue"},
		{name: "settled-only queue", items: []queue.Item{
			{ID: "COD-1", Status: queue.StatusDone},
			{ID: "COD-2", Status: queue.StatusFailed, Reason: "verify never went green"},
			{ID: "COD-3", Status: queue.StatusSkipped, Reason: "duplicate"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, root := drainServer(t, "acme")
			seedQueue(t, s, root, false, tc.items...)
			ts := httptest.NewServer(s.Handler())
			t.Cleanup(ts.Close)

			res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/drain", DrainRequest{
				Draining: true,
				NoResume: true,
				OnFault:  queue.OnFaultSkip,
			})
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("start = %d, want 409", res.StatusCode)
			}
			var out struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Error != queue.ErrNoRunnableItems.Error() {
				t.Errorf("error = %q, want the domain refusal %q", out.Error, queue.ErrNoRunnableItems.Error())
			}

			items, meta, err := s.stores.Queue(root).Snapshot()
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if meta.Draining || !meta.DrainingSince.IsZero() || meta.NoResume || meta.OnFault != "" {
				t.Errorf("meta = %+v, want untouched by the refused start", meta)
			}
			for i, it := range items {
				if it.Status != tc.items[i].Status {
					t.Errorf("%s = %q, want %q — a refused start resets nothing", it.ID, it.Status, tc.items[i].Status)
				}
			}
			s.drain.mu.Lock()
			_, active := s.drain.active[root]
			s.drain.mu.Unlock()
			if active {
				t.Error("the refused start launched a drain loop")
			}
			if len(fake.spawns) != 0 {
				t.Errorf("spawns = %d after a refused start, want none", len(fake.spawns))
			}
		})
	}
}

// Work queued after a refused start does not inherit it: the queue stays idle
// until the user starts it again.
func TestDrainEndpointStaysIdleAfterARefusedStart(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.repoLive = func(string) bool { return true }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	refused := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/drain", DrainRequest{Draining: true})
	_ = refused.Body.Close()
	if refused.StatusCode != http.StatusConflict {
		t.Fatalf("start on an empty queue = %d, want 409", refused.StatusCode)
	}

	added := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", QueueRequest{Kind: string(queue.KindTicket), ID: "COD-1"})
	_ = added.Body.Close()
	if added.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue = %d, want 201", added.StatusCode)
	}
	if drainingOf(t, s, root) {
		t.Fatal("the queue armed itself when work arrived, want it idle until an explicit start")
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawns = %d after the add, want none", len(fake.spawns))
	}

	started := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/drain", DrainRequest{Draining: true})
	_ = started.Body.Close()
	if started.StatusCode != http.StatusOK {
		t.Fatalf("start with a pending item = %d, want 200", started.StatusCode)
	}
	if !drainingOf(t, s, root) {
		t.Error("draining flag not persisted by the explicit start")
	}
}

func TestDrainEndpointRefusedForObserveOnlyRepo(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	res := postJSON(t, ts.URL+APIPrefix+"/repos/stranger/queue/drain", DrainRequest{Draining: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an observe-only repo", res.StatusCode)
	}
}

func TestDrainEndpointRejectsUnsupportedMethod(t *testing.T) {
	_, _, ts := queueServer(t, "acme")
	req, err := http.NewRequest(http.MethodGet, ts.URL+APIPrefix+"/repos/acme/queue/drain", nil)
	if err != nil {
		t.Fatalf("new GET: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET drain: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", res.StatusCode)
	}
}

// writeInstanceEntry seeds a loop's presence into the server's own store so the
// drainer reads the same database (mirroring seedQueue).
func writeInstanceEntry(t *testing.T, s *Server, e registry.Entry) {
	t.Helper()
	if err := s.stores.Instances().Upsert(e); err != nil {
		t.Fatalf("upsert instance: %v", err)
	}
}

func TestRepoHasLiveInstanceIgnoresIdle(t *testing.T) {
	cases := []struct {
		name      string
		state     string
		otherRepo bool
		blocks    bool
	}{
		{name: "idle dashboard does not block", state: registry.StateIdle, blocks: false},
		{name: "grazing loop blocks", state: registry.StateGrazing, blocks: true},
		{name: "working loop blocks", state: registry.StateWorking, blocks: true},
		{name: "parked WIP blocks", state: registry.StateParked, blocks: true},
		{name: "stopping loop blocks", state: registry.StateStopping, blocks: true},
		{name: "takeover terminal blocks", state: registry.StateTakeover, blocks: true},
		{name: "legacy entry without state blocks", state: "", blocks: true},
		{name: "working loop in another repo does not block", state: registry.StateWorking, otherRepo: true, blocks: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, root := drainServer(t, "acme")
			entryRoot := root
			if tc.otherRepo {
				entryRoot = filepath.Join(t.TempDir(), "elsewhere")
			}
			writeInstanceEntry(t, s, registry.Entry{
				PID:          os.Getpid(),
				RepoRoot:     entryRoot,
				SessionState: tc.state,
			})
			if got := s.drain.repoHasLiveInstance(root); got != tc.blocks {
				t.Errorf("repoHasLiveInstance = %v, want %v", got, tc.blocks)
			}
		})
	}
}

// TestTickTakeoverInstanceWaitsArmed pins the takeover guard on the hub spawn
// path (ADR 0018): a live takeover terminal in the repo makes the drain wait —
// not spawn, and never finish — so the queue stays armed and retries the still
// runnable item on a later tick once the lock's process dies.
func TestTickTakeoverInstanceWaitsArmed(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.repoLive = s.drain.repoHasLiveInstance
	writeInstanceEntry(t, s, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		SessionState: registry.StateTakeover,
		Ticket:       "COD-9",
	})
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"})
	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainWait {
		t.Fatalf("tick = %q, want %q — a takeover lock must make the drain wait", act, drainWait)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawned %d children, want 0 while the repo is taken over", len(fake.spawns))
	}
	if !drainingOf(t, s, root) {
		t.Error("drain disarmed — a takeover block is temporary and must stay armed")
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPending {
		t.Errorf("item status = %q, want %q", got, queue.StatusPending)
	}
}

func TestTickSpawnsDespiteIdleInstance(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.repoLive = s.drain.repoHasLiveInstance
	writeInstanceEntry(t, s, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		SessionState: registry.StateIdle,
	})
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"})
	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainSpawn {
		t.Fatalf("tick = %q, want %q — an idle instance must not hold the queue", act, drainSpawn)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawned %d children, want 1", len(fake.spawns))
	}
}

// TestReconcileQueueSweep table-drives the unsettled-item sweep over one staged
// item, across all three proofs the ticket's work already shipped: a merged
// checkpoint, a tracker that already files it done, and a PR the forge reports
// merged. Epic and ticket settle alike, fanning sub-issues out with them, and so
// does an item the queue had written off failed. An item with no proof — including
// one whose checkpoint records a fault — and anything still pending or running are
// left exactly as they were. The sweep never spawns.
func TestReconcileQueueSweep(t *testing.T) {
	tests := []struct {
		name         string
		item         queue.Item
		checkpoint   map[string]string
		issue        hubstore.Issue
		prState      string
		wantStatus   string
		wantReason   string
		wantSubState string
	}{
		{
			name: "paused epic with a merged checkpoint settles done",
			item: queue.Item{
				Kind:      queue.KindEpic,
				ID:        "COD-1",
				Status:    queue.StatusPaused,
				Reason:    "child exited without a drain report — outcome unknown",
				SubIssues: []queue.SubIssue{{ID: "COD-2", State: "backlog"}},
			},
			checkpoint:   map[string]string{"PHASE": state.Merged},
			wantStatus:   queue.StatusDone,
			wantReason:   reconciledReason(evidenceCheckpoint),
			wantSubState: "done",
		},
		{
			name:         "paused epic without a checkpoint stays parked",
			item:         queue.Item{Kind: queue.KindEpic, ID: "COD-1", Status: queue.StatusPaused, Reason: "outcome unknown", SubIssues: []queue.SubIssue{{ID: "COD-2", State: "backlog"}}},
			wantStatus:   queue.StatusPaused,
			wantReason:   "outcome unknown",
			wantSubState: "backlog",
		},
		{
			name:       "paused ticket with a merged checkpoint settles done",
			item:       queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPaused, Reason: "outcome unknown"},
			checkpoint: map[string]string{"PHASE": state.Merged},
			wantStatus: queue.StatusDone,
			wantReason: reconciledReason(evidenceCheckpoint),
		},
		{
			name: "paused item whose checkpoint records a fault stays parked",
			item: queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPaused, Reason: "unexpected error during handoff"},
			checkpoint: map[string]string{
				"PHASE":          state.HandedOff,
				"FAILURE_CLASS":  state.FailFaulted,
				"FAILURE_REASON": "unexpected error during handoff",
			},
			wantStatus: queue.StatusPaused,
			wantReason: "unexpected error during handoff",
		},
		{
			name:       "paused item the tracker already files done settles done",
			item:       queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPaused, Reason: "hub unreachable — run data could not be saved"},
			checkpoint: map[string]string{"PHASE": state.Building},
			issue:      hubstore.Issue{Identifier: "COD-1", Title: "finished out of band", Status: "Done", StatusGroup: "done"},
			wantStatus: queue.StatusDone,
			wantReason: reconciledReason(evidenceTracker),
		},
		{
			name:       "paused item the tracker still shows open stays parked",
			item:       queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPaused, Reason: "outcome unknown"},
			issue:      hubstore.Issue{Identifier: "COD-1", Title: "still open", Status: "In Progress", StatusGroup: "started"},
			wantStatus: queue.StatusPaused,
			wantReason: "outcome unknown",
		},
		{
			name: "paused epic whose PR merged settles done",
			item: queue.Item{
				Kind:      queue.KindEpic,
				ID:        "COD-1",
				Status:    queue.StatusPaused,
				Reason:    "unexpected error during finalize",
				SubIssues: []queue.SubIssue{{ID: "COD-2", State: "backlog"}},
			},
			checkpoint: map[string]string{
				"PHASE":          state.PROpen,
				"PR":             "42",
				"FAILURE_CLASS":  state.FailFaulted,
				"FAILURE_REASON": "unexpected error during finalize",
			},
			prState:      "MERGED",
			wantStatus:   queue.StatusDone,
			wantReason:   reconciledReason(evidencePR),
			wantSubState: "done",
		},
		{
			name:       "paused item whose PR is still open stays parked",
			item:       queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusPaused, Reason: "outcome unknown"},
			checkpoint: map[string]string{"PHASE": state.PROpen, "PR": "42"},
			prState:    "OPEN",
			wantStatus: queue.StatusPaused,
			wantReason: "outcome unknown",
		},
		{
			name:       "failed item the tracker files done settles done",
			item:       queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusFailed, Reason: "unexpected error during verify"},
			issue:      hubstore.Issue{Identifier: "COD-1", Title: "shipped anyway", Status: "Done", StatusGroup: "done"},
			wantStatus: queue.StatusDone,
			wantReason: reconciledReason(evidenceTracker),
		},
		{
			name:       "pending item is untouched",
			item:       queue.Item{Kind: queue.KindTicket, ID: "COD-1"},
			checkpoint: map[string]string{"PHASE": state.Merged},
			wantStatus: queue.StatusPending,
		},
		{
			name:       "running item is untouched",
			item:       queue.Item{Kind: queue.KindTicket, ID: "COD-1", Status: queue.StatusRunning, PID: 7},
			checkpoint: map[string]string{"PHASE": state.Merged},
			wantStatus: queue.StatusRunning,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, root := drainServer(t, "acme")
			s.drain.outcome = s.drain.checkpointOutcome
			s.drain.prState = func(string, string) string { return tc.prState }
			if tc.checkpoint != nil {
				if err := s.stores.Checkpoints().Upsert(root, tc.item.ID, tc.checkpoint); err != nil {
					t.Fatalf("seed checkpoint: %v", err)
				}
			}
			if tc.issue.Identifier != "" {
				if _, _, err := s.stores.Issues().Upsert(root, "linear", []hubstore.Issue{tc.issue}); err != nil {
					t.Fatalf("seed issue: %v", err)
				}
			}
			seedQueue(t, s, root, false, tc.item)

			s.drain.reconcileQueue(root)

			if got := statusOf(t, s, root, tc.item.ID); got != tc.wantStatus {
				t.Errorf("%s status = %q, want %q", tc.item.ID, got, tc.wantStatus)
			}
			if got := reasonOf(t, s, root, tc.item.ID); got != tc.wantReason {
				t.Errorf("%s reason = %q, want %q", tc.item.ID, got, tc.wantReason)
			}
			if tc.wantSubState != "" {
				for _, it := range snapshot(t, s, root) {
					for _, sub := range it.SubIssues {
						if sub.State != tc.wantSubState {
							t.Errorf("sub %s state = %q, want %q", sub.ID, sub.State, tc.wantSubState)
						}
					}
				}
			}
			if len(fake.spawns) != 0 {
				t.Errorf("spawns = %d, want none — the sweep settles from evidence and never runs anything", len(fake.spawns))
			}
		})
	}
}

// TestQueueStartReconcilesBeforeFirstSpawn proves the arm-time hook: a Start over
// a queue whose head is parked but already merged settles that item within the
// request, so the drain's first spawn is the item behind it rather than a re-run
// of work that shipped.
func TestQueueStartReconcilesBeforeFirstSpawn(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.outcome = s.drain.checkpointOutcome
	// Occupy the repo's drain slot so ensure is a no-op and the test — not a
	// background loop — drives the tick that follows the reconcile.
	s.drain.mu.Lock()
	s.drain.active[root] = func() {}
	s.drain.mu.Unlock()
	if err := s.stores.Checkpoints().Upsert(root, "COD-1", map[string]string{"PHASE": state.Merged}); err != nil {
		t.Fatalf("seed merged checkpoint: %v", err)
	}
	seedQueue(t, s, root, false,
		queue.Item{Kind: queue.KindEpic, ID: "COD-1", Status: queue.StatusPaused, Reason: "outcome unknown"},
		queue.Item{Kind: queue.KindTicket, ID: "COD-2"},
	)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue/drain", DrainRequest{Draining: true})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start = %d, want 200", res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Items[0].Status != queue.StatusDone {
		t.Fatalf("COD-1 = %q in the start response, want done — the reconcile runs inside the Start", out.Items[0].Status)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawns = %d during the Start, want none", len(fake.spawns))
	}

	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("first tick = %q, want spawn", act)
	}
	running, ok := runningItem(t, s, root)
	if !ok || running.ID != "COD-2" {
		t.Fatalf("first spawn ran %+v, want COD-2 — the settled item must not re-run", running)
	}
}

// TestServerStartSettlesParkedMergedEpic is the COD-1161 acceptance, in the
// COD-1151 shape: a hub comes back to an epic item parked with an outcome-unknown
// reason while its checkpoint has read merged the whole time. Boot settles it done
// with its sub-issues, records the evidence in the feed, and starts no child.
func TestServerStartSettlesParkedMergedEpic(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	s.drain.outcome = s.drain.checkpointOutcome
	if err := s.stores.Checkpoints().Upsert(root, "COD-1151", map[string]string{"PHASE": state.Merged}); err != nil {
		t.Fatalf("seed merged checkpoint: %v", err)
	}
	seedQueue(t, s, root, false, queue.Item{
		Kind:      queue.KindEpic,
		ID:        "COD-1151",
		Status:    queue.StatusPaused,
		Reason:    "child exited without a drain report — outcome unknown",
		SubIssues: []queue.SubIssue{{ID: "COD-1152", State: "backlog"}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.Start(ctx, 0, 0)

	if got := statusOf(t, s, root, "COD-1151"); got != queue.StatusDone {
		t.Fatalf("COD-1151 = %q after boot, want done — its checkpoint proved the work shipped", got)
	}
	if got := reasonOf(t, s, root, "COD-1151"); got != reconciledReason(evidenceCheckpoint) {
		t.Errorf("COD-1151 reason = %q, want the settle evidence %q", got, reconciledReason(evidenceCheckpoint))
	}
	for _, it := range snapshot(t, s, root) {
		for _, sub := range it.SubIssues {
			if sub.State != "done" {
				t.Errorf("sub %s state = %q, want done with the settled epic", sub.ID, sub.State)
			}
		}
	}
	if len(fake.spawns) != 0 {
		t.Errorf("spawns = %d on boot, want none", len(fake.spawns))
	}

	rows, err := s.stores.Events().Recent(root, 10, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var msg string
	for _, row := range rows {
		if row.Kind == event.KindQueueReconciled {
			msg = row.Msg
		}
	}
	if msg == "" {
		t.Fatal("no queue_reconciled event, want the settle explained in the feed")
	}
	if !strings.Contains(msg, "COD-1151") || !strings.Contains(msg, "checkpoint merged") {
		t.Errorf("event msg = %q, want it to name the item and cite the checkpoint evidence", msg)
	}
}
