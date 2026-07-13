package hubstore

import (
	"reflect"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

func testInstances(t *testing.T) *Instances {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(db.SQL(), nil, Retention{}).Instances()
}

func countInstances(t *testing.T, in *Instances) int {
	t.Helper()
	var n int
	if err := in.db.QueryRow(`SELECT COUNT(*) FROM instances`).Scan(&n); err != nil {
		t.Fatalf("count instances: %v", err)
	}
	return n
}

func TestInstancesUpsertRoundTripsAndRemoves(t *testing.T) {
	in := testInstances(t)
	in.alive = func(int) bool { return true }

	start := time.Now().Add(-2 * time.Minute)
	since := time.Now().Add(-30 * time.Second)
	e := registry.Entry{
		PID:          4242,
		RepoRoot:     "/repo/acme",
		RunsDir:      "/repo/acme/.trau/runs",
		StartedAt:    start,
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "COD-42",
		Phase:        state.Building,
		StateSince:   since,
	}
	if err := in.Upsert(e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	live, err := in.Live()
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("Live = %d entries, want 1", len(live))
	}
	got := live[0]
	if got.PID != 4242 || got.RepoRoot != "/repo/acme" || got.RunsDir != "/repo/acme/.trau/runs" {
		t.Errorf("identity = %+v, want the seeded entry", got)
	}
	if got.SessionState != registry.StateWorking || got.Ticket != "COD-42" || got.Phase != state.Building {
		t.Errorf("reported state = {%s %s %s}, want working/COD-42/building", got.SessionState, got.Ticket, got.Phase)
	}
	if !got.StartedAt.Equal(start) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, start)
	}
	if !got.StateSince.Equal(since) {
		t.Errorf("StateSince = %v, want %v", got.StateSince, since)
	}

	// A second upsert on the same PID refreshes in place — presence is keyed by PID.
	e.SessionState = registry.StateParked
	e.Phase = ""
	if err := in.Upsert(e); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	if n := countInstances(t, in); n != 1 {
		t.Fatalf("rows after re-upsert = %d, want 1", n)
	}

	if err := in.Remove(4242); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if n := countInstances(t, in); n != 0 {
		t.Fatalf("rows after Remove = %d, want 0", n)
	}
	if err := in.Remove(4242); err != nil {
		t.Fatalf("Remove is not idempotent: %v", err)
	}
}

func TestInstancesActivityRoundTrip(t *testing.T) {
	in := testInstances(t)
	in.alive = func(int) bool { return true }

	e := registry.Entry{
		PID:          7,
		RepoRoot:     "/repo/acme",
		StartedAt:    time.Now(),
		Heartbeat:    time.Now(),
		SessionState: registry.StateWorking,
		Ticket:       "COD-9",
		Phase:        state.HandedOff,
		Activity:     "repair",
		Detail:       "repair2",
	}
	if err := in.Upsert(e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	live, err := in.Live()
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("Live = %d entries, want 1", len(live))
	}
	if got := live[0]; got.Activity != "repair" || got.Detail != "repair2" {
		t.Errorf("activity/detail = %q/%q, want repair/repair2", got.Activity, got.Detail)
	}

	// A later report advances the activity and clears the label; the columns update
	// in place under the PID key.
	e.Activity = "ci-wait"
	e.Detail = ""
	if err := in.Upsert(e); err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	live, err = in.Live()
	if err != nil {
		t.Fatalf("Live after re-upsert: %v", err)
	}
	if got := live[0]; got.Activity != "ci-wait" || got.Detail != "" {
		t.Errorf("activity/detail after advance = %q/%q, want ci-wait/empty", got.Activity, got.Detail)
	}
}

func TestInstancesLiveReapsDeadPID(t *testing.T) {
	in := testInstances(t)
	in.alive = func(pid int) bool { return pid == 100 }

	if err := in.Upsert(registry.Entry{PID: 100, RepoRoot: "/repo/live", StartedAt: time.Now()}); err != nil {
		t.Fatalf("Upsert live: %v", err)
	}
	if err := in.Upsert(registry.Entry{PID: 200, RepoRoot: "/repo/dead", StartedAt: time.Now()}); err != nil {
		t.Fatalf("Upsert dead: %v", err)
	}

	live, err := in.Live()
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 1 || live[0].PID != 100 {
		t.Fatalf("Live = %+v, want only pid 100", live)
	}
	// The dead PID's row is deleted, not merely filtered — no stale entry lingers.
	if n := countInstances(t, in); n != 1 {
		t.Fatalf("rows after reap = %d, want 1 (dead row deleted)", n)
	}
}

func TestInstancesLiveOrdersByStart(t *testing.T) {
	in := testInstances(t)
	in.alive = func(int) bool { return true }

	base := time.Now()
	for pid, start := range map[int]time.Time{
		1: base.Add(2 * time.Minute),
		2: base,
		3: base.Add(time.Minute),
	} {
		if err := in.Upsert(registry.Entry{PID: pid, StartedAt: start}); err != nil {
			t.Fatalf("Upsert pid %d: %v", pid, err)
		}
	}

	live, err := in.Live()
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	order := make([]int, 0, len(live))
	for _, e := range live {
		order = append(order, e.PID)
	}
	if want := []int{2, 3, 1}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v (oldest first)", order, want)
	}
}
