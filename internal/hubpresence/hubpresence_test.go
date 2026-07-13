package hubpresence

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/registry"
)

type fakeClient struct {
	mu      sync.Mutex
	puts    []hubclient.InstanceHeartbeat
	deletes int
}

func (f *fakeClient) PutInstance(_ context.Context, _ int, hb hubclient.InstanceHeartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, hb)
	return nil
}

func (f *fakeClient) DeleteInstance(context.Context, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	return nil
}

func (f *fakeClient) last() (hubclient.InstanceHeartbeat, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.puts) == 0 {
		return hubclient.InstanceHeartbeat{}, false
	}
	return f.puts[len(f.puts)-1], true
}

func (f *fakeClient) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 1s")
}

func TestRegisterFlushesIdleThenDeregisters(t *testing.T) {
	f := &fakeClient{}
	h := Register(f, "/repo/acme", ".trau/runs")

	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.SessionState == registry.StateIdle
	})
	hb, _ := f.last()
	if hb.RepoRoot != "/repo/acme" {
		t.Errorf("RepoRoot = %q, want /repo/acme", hb.RepoRoot)
	}
	if want := "/repo/acme/.trau/runs"; hb.RunsDir != want {
		t.Errorf("RunsDir = %q, want %q (relative resolved against repo root)", hb.RunsDir, want)
	}
	if hb.StartedAt.IsZero() || hb.StateSince.IsZero() {
		t.Errorf("StartedAt/StateSince unset: %+v", hb)
	}

	h.Deregister()
	if got := f.deleteCount(); got != 1 {
		t.Errorf("DeleteInstance calls = %d, want 1", got)
	}
	h.Deregister()
	if got := f.deleteCount(); got != 1 {
		t.Errorf("DeleteInstance calls after a second Deregister = %d, want 1 (idempotent)", got)
	}
}

func TestSetStateFlushesReportedActivity(t *testing.T) {
	f := &fakeClient{}
	h := Register(f, "/repo/acme", "")
	defer h.Deregister()

	h.SetState(registry.StateWorking, "COD-9", "building")
	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.SessionState == registry.StateWorking && hb.Ticket == "COD-9" && hb.Phase == "building"
	})
	working, _ := f.last()

	h.SetState(registry.StateWorking, "COD-9", "testing")
	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.Phase == "testing"
	})
	testing2, _ := f.last()
	if !testing2.StateSince.After(working.StateSince) {
		t.Errorf("StateSince did not advance on a phase change: %v then %v", working.StateSince, testing2.StateSince)
	}
}

func TestSetActivityFlushesAndAdvancesStateSince(t *testing.T) {
	f := &fakeClient{}
	h := Register(f, "/repo/acme", "")
	defer h.Deregister()

	h.SetState(registry.StateWorking, "COD-9", "building")
	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.SessionState == registry.StateWorking
	})

	h.SetActivity("build", "")
	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.Activity == "build"
	})
	build, _ := f.last()
	if build.Detail != "" {
		t.Errorf("Detail = %q, want empty for a label-less activity", build.Detail)
	}

	h.SetActivity("repair", "repair1")
	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.Activity == "repair" && hb.Detail == "repair1"
	})
	repair, _ := f.last()
	if !repair.StateSince.After(build.StateSince) {
		t.Errorf("StateSince did not advance on an activity change: %v then %v", build.StateSince, repair.StateSince)
	}
	if repair.Ticket != "COD-9" || repair.Phase != "building" {
		t.Errorf("activity change disturbed the reported ticket/phase: %+v", repair)
	}
}

func TestNonWorkingStateClearsActivity(t *testing.T) {
	f := &fakeClient{}
	h := Register(f, "/repo/acme", "")
	defer h.Deregister()

	h.SetState(registry.StateWorking, "COD-9", "building")
	h.SetActivity("build", "notes")
	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.Activity == "build" && hb.Detail == "notes"
	})

	h.SetState(registry.StateIdle, "", "")
	waitFor(t, func() bool {
		hb, ok := f.last()
		return ok && hb.SessionState == registry.StateIdle
	})
	idle, _ := f.last()
	if idle.Activity != "" || idle.Detail != "" {
		t.Errorf("idle report kept activity/detail: %q/%q, want both cleared", idle.Activity, idle.Detail)
	}
}

func TestNilAndUnregisteredHandleNoOp(t *testing.T) {
	var nilHandle *Handle
	nilHandle.SetState(registry.StateWorking, "COD-1", "building")
	nilHandle.SetActivity("build", "")
	nilHandle.Deregister()

	unregistered := Register(nil, "/repo/acme", "")
	unregistered.SetState(registry.StateWorking, "COD-1", "building")
	unregistered.SetActivity("build", "")
	unregistered.Deregister()
}
