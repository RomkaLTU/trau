package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/state"
)

// drainServer builds a server whose allowlist holds one Registered repo, with a
// fake supervisor and deterministic drain probes so a tick's decision is a pure
// function of the seeded queue rather than of real processes. Tests override the
// probes per case.
func drainServer(t *testing.T, name string) (*Server, *fakeSupervisor, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false)
	s.home = t.TempDir()
	fake := &fakeSupervisor{}
	s.sup = fake
	s.drain.repoLive = func(string) bool { return false }
	s.drain.alive = func(int) bool { return false }
	s.drain.outcome = func(string, queue.Item) (string, string) { return "", "" }
	return s, fake, root
}

// seedQueue writes a queue file through the store's own API so a case can stage
// items already running or finished, then sets the draining flag.
func seedQueue(t *testing.T, root string, draining bool, items ...queue.Item) {
	t.Helper()
	st := queue.NewStore(root)
	for _, it := range items {
		base := queue.Item{Kind: it.Kind, ID: it.ID, Title: it.Title, SubIssues: it.SubIssues}
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
		}
	}
	if err := st.SetDraining(draining); err != nil {
		t.Fatalf("seed draining: %v", err)
	}
}

func snapshot(t *testing.T, root string) []queue.Item {
	t.Helper()
	items, _, err := queue.NewStore(root).Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return items
}

func statusOf(t *testing.T, root, id string) string {
	t.Helper()
	for _, it := range snapshot(t, root) {
		if it.ID == id {
			return it.Status
		}
	}
	t.Fatalf("item %s missing from queue", id)
	return ""
}

func reasonOf(t *testing.T, root, id string) string {
	t.Helper()
	for _, it := range snapshot(t, root) {
		if it.ID == id {
			return it.Reason
		}
	}
	t.Fatalf("item %s missing from queue", id)
	return ""
}

func drainingOf(t *testing.T, root string) bool {
	t.Helper()
	_, draining, err := queue.NewStore(root).Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return draining
}

func countStatus(t *testing.T, root, status string) int {
	t.Helper()
	n := 0
	for _, it := range snapshot(t, root) {
		if it.Status == status {
			n++
		}
	}
	return n
}

func runningItem(t *testing.T, root string) (queue.Item, bool) {
	for _, it := range snapshot(t, root) {
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
// guarantee, waiting on an external live run, and pausing.
func TestDrainTickDecisions(t *testing.T) {
	tests := []struct {
		name          string
		items         []queue.Item
		draining      bool
		alive         map[int]bool
		repoLive      bool
		outcomeClass  string
		outcomeReason string
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
			name:       "settles a finished child to done",
			items:      []queue.Item{{ID: "COD-1", Status: queue.StatusRunning, PID: 7}},
			draining:   true,
			wantAction: drainReconcile,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusDone},
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
			wantAction: drainReconcile,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusDone},
		},
		{
			name:       "idles when the queue is drained but still running",
			items:      []queue.Item{{ID: "COD-1", Status: queue.StatusDone}},
			draining:   true,
			wantAction: drainWait,
			wantSpawns: 0,
			wantStatus: map[string]string{"COD-1": queue.StatusDone},
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
			seedQueue(t, root, tc.draining, tc.items...)

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
				if got := statusOf(t, root, id); got != want {
					t.Errorf("%s status = %q, want %q", id, got, want)
				}
			}
			for id, want := range tc.wantReason {
				if got := reasonOf(t, root, id); got != want {
					t.Errorf("%s reason = %q, want %q", id, got, want)
				}
			}
			if tc.wantDraining != nil {
				if got := drainingOf(t, root); got != *tc.wantDraining {
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
		wantStatus string
		wantPause  bool
	}{
		{name: "clean finish settles done", class: "", wantStatus: queue.StatusDone, wantPause: false},
		{name: "give-up settles failed and drains on", class: state.FailGaveUp, wantStatus: queue.StatusFailed, wantPause: false},
		{name: "fault pauses the queue", class: state.FailFaulted, wantStatus: queue.StatusPaused, wantPause: true},
		{name: "provider pause pauses the queue", class: state.FailPaused, wantStatus: queue.StatusPaused, wantPause: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, pause := classifyDrainOutcome(tc.class)
			if status != tc.wantStatus || pause != tc.wantPause {
				t.Errorf("classifyDrainOutcome(%q) = (%q, %v), want (%q, %v)", tc.class, status, pause, tc.wantStatus, tc.wantPause)
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, root := drainServer(t, "acme")
			st := state.NewStore(filepath.Join(root, ".trau", "runs"))
			if err := st.Set("COD-1", "PHASE", tc.phase); err != nil {
				t.Fatalf("seed phase: %v", err)
			}
			if tc.failClass != "" {
				if err := st.Set("COD-1", "FAILURE_CLASS", tc.failClass); err != nil {
					t.Fatalf("seed class: %v", err)
				}
			}
			if tc.reason != "" {
				if err := st.Set("COD-1", "FAILURE_REASON", tc.reason); err != nil {
					t.Fatalf("seed reason: %v", err)
				}
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
	seedQueue(t, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 7},
		queue.Item{ID: "COD-2"},
	)

	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("settle tick = %q, want reconcile", act)
	}
	if got := statusOf(t, root, "COD-1"); got != queue.StatusPaused {
		t.Fatalf("COD-1 = %q, want paused", got)
	}
	if got := reasonOf(t, root, "COD-1"); got != reason {
		t.Errorf("COD-1 reason = %q, want the fault reason", got)
	}
	if drainingOf(t, root) {
		t.Error("queue still draining after a fault, want it paused")
	}

	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick while paused = %q, want stop", act)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("spawns = %d while paused, want none", len(fake.spawns))
	}

	class = ""
	if err := queue.NewStore(root).SetDraining(true); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("resume tick = %q, want it to re-attempt the paused item", act)
	}
	running, ok := runningItem(t, root)
	if !ok || running.ID != "COD-1" {
		t.Fatalf("re-attempted item = %+v, want COD-1", running)
	}
	if running.Reason != "" {
		t.Errorf("re-attempted COD-1 reason = %q, want it cleared", running.Reason)
	}
	if got := statusOf(t, root, "COD-2"); got != queue.StatusPending {
		t.Errorf("COD-2 = %q, want it still pending behind COD-1", got)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once"})
}

// TestDrainRunsSequentially drives a full drain of three items to completion,
// asserting they spawn in queue order and that exactly one child is ever running
// at a time.
func TestDrainRunsSequentially(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	alive := map[int]bool{}
	s.drain.alive = func(pid int) bool { return alive[pid] }
	seedQueue(t, root, true,
		queue.Item{ID: "COD-1"},
		queue.Item{ID: "COD-2"},
		queue.Item{Kind: queue.KindEpic, ID: "COD-3"},
	)

	var order []string
	for step := 0; step < 30 && countStatus(t, root, queue.StatusDone) < 3; step++ {
		act, err := s.drain.tick(root)
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		switch act {
		case drainSpawn:
			it, ok := runningItem(t, root)
			if !ok {
				t.Fatal("spawn reported but nothing is running")
			}
			order = append(order, it.ID)
			if n := countStatus(t, root, queue.StatusRunning); n != 1 {
				t.Fatalf("running items = %d after a spawn, want exactly 1", n)
			}
			alive[it.PID] = true
		case drainWait:
			if it, ok := runningItem(t, root); ok {
				alive[it.PID] = false
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
	if done := countStatus(t, root, queue.StatusDone); done != 3 {
		t.Errorf("done = %d, want all three settled", done)
	}
	assertArgs(t, fake.spawns[0].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-1", "--once"})
	assertArgs(t, fake.spawns[2].Args, []string{"--repo", root, "--no-tui", "--parent", "COD-3"})
}

// TestDrainPauseTakesEffectAfterCurrentChild pauses while a child is in flight:
// the running item still settles, the queue then stops, and the next item is
// left pending for a later start.
func TestDrainPauseTakesEffectAfterCurrentChild(t *testing.T) {
	s, _, root := drainServer(t, "acme")
	alive := map[int]bool{}
	s.drain.alive = func(pid int) bool { return alive[pid] }
	seedQueue(t, root, true, queue.Item{ID: "COD-1"}, queue.Item{ID: "COD-2"})

	if act, _ := s.drain.tick(root); act != drainSpawn {
		t.Fatalf("first tick = %q, want spawn", act)
	}
	running, _ := runningItem(t, root)
	alive[running.PID] = true

	if err := queue.NewStore(root).SetDraining(false); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if act, _ := s.drain.tick(root); act != drainWait {
		t.Fatalf("tick while the child runs = %q, want wait (no mid-run kill)", act)
	}
	if statusOf(t, root, "COD-1") != queue.StatusRunning {
		t.Error("COD-1 must keep running until its child exits")
	}

	alive[running.PID] = false
	if act, _ := s.drain.tick(root); act != drainReconcile {
		t.Fatalf("tick after the child exits = %q, want reconcile", act)
	}
	if act, _ := s.drain.tick(root); act != drainStop {
		t.Fatalf("tick once settled = %q, want stop (pause took effect)", act)
	}
	if statusOf(t, root, "COD-1") != queue.StatusDone {
		t.Error("COD-1 should be settled done")
	}
	if statusOf(t, root, "COD-2") != queue.StatusPending {
		t.Error("COD-2 should stay pending for a later start")
	}
}

// TestDrainResumeSettlesLeftoverRunning is the restart case: a hub comes up with
// an item persisted as running whose child is already gone, and resumes the
// repo so the item is settled and the queue continues.
func TestDrainResumeSettlesLeftoverRunning(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	first := New("1.2.3", "127.0.0.1", "", []string{root}, false)
	first.home = t.TempDir()
	first.sup = &fakeSupervisor{}
	seedQueue(t, root, true,
		queue.Item{ID: "COD-1", Status: queue.StatusRunning, PID: 999999},
		queue.Item{ID: "COD-2"},
	)

	second := New("1.2.3", "127.0.0.1", "", []string{root}, false)
	second.home = first.home
	second.sup = &fakeSupervisor{}
	second.drain.alive = func(int) bool { return false }
	second.drain.repoLive = func(string) bool { return false }

	if _, running := firstWithStatus(snapshot(t, root), queue.StatusRunning); !running {
		t.Fatal("precondition: COD-1 should be persisted as running")
	}
	if act, _ := second.drain.tick(root); act != drainReconcile {
		t.Fatalf("first resumed tick = %q, want it to settle the leftover run", act)
	}
	if statusOf(t, root, "COD-1") != queue.StatusDone {
		t.Errorf("leftover COD-1 = %q, want settled done", statusOf(t, root, "COD-1"))
	}
	if act, _ := second.drain.tick(root); act != drainSpawn {
		t.Fatalf("next tick = %q, want it to continue with COD-2", act)
	}
	if statusOf(t, root, "COD-2") != queue.StatusRunning {
		t.Error("COD-2 should now be running")
	}
}

func TestDrainEndpointStartsAndPauses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme")
	s := New("1.2.3", "127.0.0.1", "", []string{root}, false)
	s.home = t.TempDir()
	s.sup = &fakeSupervisor{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.drainCtx = ctx
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
	if _, draining, _ := queue.NewStore(root).Snapshot(); !draining {
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
	if _, draining, _ := queue.NewStore(root).Snapshot(); draining {
		t.Error("draining flag not cleared after pause")
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
