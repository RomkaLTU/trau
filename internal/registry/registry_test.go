package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// deadPID starts a child, kills and reaps it, and returns a PID that is now
// guaranteed not to name a running process.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return pid
}

func TestRegisterAppearsLiveThenDeregisters(t *testing.T) {
	home := t.TempDir()

	h := Register(home, "/repo/acme", "/repo/acme/.trau/runs")
	live := Live(home)
	if len(live) != 1 {
		t.Fatalf("Live after Register = %d entries, want 1", len(live))
	}
	if live[0].PID != os.Getpid() {
		t.Errorf("entry PID = %d, want own %d", live[0].PID, os.Getpid())
	}
	if live[0].RepoRoot != "/repo/acme" {
		t.Errorf("RepoRoot = %q, want /repo/acme", live[0].RepoRoot)
	}

	h.Deregister()
	if live := Live(home); len(live) != 0 {
		t.Fatalf("Live after Deregister = %d entries, want 0", len(live))
	}
}

func TestLiveReapsDeadEntries(t *testing.T) {
	home := t.TempDir()
	Register(home, "/repo/live", ".trau/runs")

	dead := Entry{PID: deadPID(t), RepoRoot: "/repo/dead", RunsDir: "/repo/dead/.trau/runs"}
	deadFile := filepath.Join(instancesDir(home), entryName(dead.PID))
	if err := writeJSON(deadFile, dead); err != nil {
		t.Fatalf("write dead entry: %v", err)
	}

	live := Live(home)
	if len(live) != 1 {
		t.Fatalf("Live = %d entries, want 1 (the dead one reaped)", len(live))
	}
	if live[0].PID != os.Getpid() {
		t.Errorf("surviving PID = %d, want own %d", live[0].PID, os.Getpid())
	}
	if _, err := os.Stat(deadFile); !os.IsNotExist(err) {
		t.Errorf("dead entry file still present, want reaped")
	}
}

func TestRegisterIsBestEffortWhenUnwritable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	home := filepath.Join(blocker, "trau") // parent is a file: every write fails

	h := Register(home, "/repo/acme", ".trau/runs")
	if live := Live(home); len(live) != 0 {
		t.Fatalf("Live over unwritable home = %d, want 0", len(live))
	}
	h.Deregister() // must not panic
}

func TestRelativeRunsDirResolvedAgainstRepoRoot(t *testing.T) {
	home := t.TempDir()
	Register(home, "/repo/acme", ".trau/runs")

	live := Live(home)
	if len(live) != 1 {
		t.Fatalf("Live = %d entries, want 1", len(live))
	}
	if want := "/repo/acme/.trau/runs"; live[0].RunsDir != want {
		t.Errorf("RunsDir = %q, want %q", live[0].RunsDir, want)
	}
}

func TestRegisterSeedsIdle(t *testing.T) {
	home := t.TempDir()
	h := Register(home, "/repo/acme", ".trau/runs")
	defer h.Deregister()

	e := Live(home)[0]
	if e.SessionState != StateIdle {
		t.Errorf("SessionState = %q, want %q", e.SessionState, StateIdle)
	}
	if e.StateSince.IsZero() {
		t.Errorf("StateSince is zero, want stamped at registration")
	}
}

func TestSetStatePersistsAllFields(t *testing.T) {
	home := t.TempDir()
	h := Register(home, "/repo/acme", ".trau/runs")
	defer h.Deregister()

	h.SetState(StateWorking, "COD-765", "building")
	e := Live(home)[0]
	if e.SessionState != StateWorking || e.Ticket != "COD-765" || e.Phase != "building" {
		t.Fatalf("after SetState = %+v, want working/COD-765/building", e)
	}
	since := e.StateSince
	if since.IsZero() {
		t.Fatal("StateSince is zero after SetState")
	}

	h.SetState(StateWorking, "COD-765", "testing")
	if e := Live(home)[0]; e.StateSince.Equal(since) {
		t.Errorf("StateSince did not move on a phase change: %v", since)
	}

	h.SetState(StateParked, "COD-765", "")
	e = Live(home)[0]
	if e.SessionState != StateParked || e.Phase != "" {
		t.Errorf("after park = %+v, want parked with no phase", e)
	}
	if e.StateSince.Equal(since) {
		t.Errorf("StateSince did not move on a state change")
	}
}

func TestSetStateSurvivesConcurrentHeartbeat(t *testing.T) {
	home := t.TempDir()
	h := Register(home, "/repo/acme", ".trau/runs")
	defer h.Deregister()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.mu.Lock()
			h.entry.Heartbeat = time.Now()
			_ = writeJSON(h.path, h.entry)
			h.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.SetState(StateWorking, "COD-765", "building")
		}
	}()
	wg.Wait()

	if e := Live(home)[0]; e.SessionState != StateWorking || e.Ticket != "COD-765" || e.Phase != "building" {
		t.Errorf("after concurrent beat+SetState = %+v, want working state intact", e)
	}
}

func TestSetStateNilAndUnregisteredHandleNoOp(t *testing.T) {
	var nilHandle *Handle
	nilHandle.SetState(StateWorking, "COD-765", "building")

	unregistered := &Handle{}
	unregistered.SetState(StateWorking, "COD-765", "building")
	if unregistered.entry.SessionState != "" {
		t.Errorf("unregistered SetState mutated entry: %+v", unregistered.entry)
	}
}
