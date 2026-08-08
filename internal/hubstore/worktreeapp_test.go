package hubstore

import (
	"errors"
	"testing"
)

// TestWorktreeAppStartsStopped pins what a freshly recorded tree says about its
// app: nothing is serving until something starts one, which is also what every row
// migrated from before the app columns existed must read as.
func TestWorktreeAppStartsStopped(t *testing.T) {
	w := testWorktrees(t)

	row, err := w.Record("/repo/acme", WorktreeInput{Ticket: "COD-1", Path: "/w/acme/COD-1"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if row.App != (WorktreeApp{State: AppStopped}) {
		t.Errorf("app = %+v, want a stopped app with nothing allocated", row.App)
	}
}

func TestWorktreeSetAppRoundTrips(t *testing.T) {
	w := testWorktrees(t)
	row, err := w.Record("/repo/acme", WorktreeInput{Ticket: "COD-1", Path: "/w/acme/COD-1"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	app := WorktreeApp{Port: 4300, PID: 4242, State: AppRunning, StartedAt: "2026-08-08T10:00:00Z"}
	updated, err := w.SetApp("/repo/acme", row.ID, app)
	if err != nil {
		t.Fatalf("set app: %v", err)
	}
	if updated.App != app {
		t.Fatalf("app = %+v, want %+v", updated.App, app)
	}

	read, ok, err := w.ByTicket("/repo/acme", "COD-1")
	if err != nil || !ok {
		t.Fatalf("read back: %v (found %v)", err, ok)
	}
	if read.App != app {
		t.Errorf("re-read app = %+v, want %+v", read.App, app)
	}
}

// TestWorktreeRecordLeavesARunningAppAlone is the resume case: a child re-reporting
// the tree it adopted knows nothing about the app the hub serves out of it, and
// must not reset a live app to stopped.
func TestWorktreeRecordLeavesARunningAppAlone(t *testing.T) {
	w := testWorktrees(t)
	row, err := w.Record("/repo/acme", WorktreeInput{Ticket: "COD-1", Path: "/w/acme/COD-1"})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	app := WorktreeApp{Port: 4300, PID: 4242, State: AppRunning, StartedAt: "2026-08-08T10:00:00Z"}
	if _, err := w.SetApp("/repo/acme", row.ID, app); err != nil {
		t.Fatalf("set app: %v", err)
	}

	again, err := w.Record("/repo/acme", WorktreeInput{
		Ticket: "COD-1", Path: "/w/acme/COD-1", Branch: "feature/COD-1-x",
	})
	if err != nil {
		t.Fatalf("re-record: %v", err)
	}
	if again.App != app {
		t.Errorf("app after re-record = %+v, want the running app %+v", again.App, app)
	}
}

func TestWorktreeSetAppRejectsAnUnknownRow(t *testing.T) {
	w := testWorktrees(t)
	if _, err := w.SetApp("/repo/acme", 404, WorktreeApp{State: AppRunning}); !errors.Is(err, ErrWorktreeNotFound) {
		t.Fatalf("err = %v, want ErrWorktreeNotFound", err)
	}
}

// TestWorktreeHeldPortsCountsOnlyLiveApps is what the port allocator relies on: a
// starting app already owns its port, while a stopped or failed one has given it
// back and must not push the next tree further up the range.
func TestWorktreeHeldPortsCountsOnlyLiveApps(t *testing.T) {
	w := testWorktrees(t)
	seed := func(repo, ticket string, app WorktreeApp) {
		t.Helper()
		row, err := w.Record(repo, WorktreeInput{Ticket: ticket, Path: "/w/" + ticket})
		if err != nil {
			t.Fatalf("record %s: %v", ticket, err)
		}
		if _, err := w.SetApp(repo, row.ID, app); err != nil {
			t.Fatalf("set app %s: %v", ticket, err)
		}
	}
	seed("/repo/acme", "COD-1", WorktreeApp{Port: 4300, PID: 1, State: AppRunning})
	seed("/repo/acme", "COD-2", WorktreeApp{Port: 4301, PID: 2, State: AppStarting})
	seed("/repo/acme", "COD-3", WorktreeApp{Port: 4302, State: AppFailed})
	// A second repo's live app holds its port just as much: ports are a machine's,
	// not a repo's.
	seed("/repo/other", "OTH-1", WorktreeApp{Port: 4303, PID: 3, State: AppRunning})

	held, err := w.HeldPorts()
	if err != nil {
		t.Fatalf("held ports: %v", err)
	}
	want := map[int]bool{4300: true, 4301: true, 4303: true}
	if len(held) != len(want) {
		t.Fatalf("held = %v, want %v", held, want)
	}
	for port := range want {
		if !held[port] {
			t.Errorf("port %d is not held, want it held", port)
		}
	}
	if held[4302] {
		t.Errorf("the failed app's port 4302 reads as held")
	}
}
