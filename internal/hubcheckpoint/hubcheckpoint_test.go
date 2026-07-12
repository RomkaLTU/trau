package hubcheckpoint

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
)

type fakeHub struct {
	mu    sync.Mutex
	store map[string]hubclient.Checkpoint
	puts  int
}

func newFakeHub() *fakeHub { return &fakeHub{store: map[string]hubclient.Checkpoint{}} }

func (f *fakeHub) PutCheckpoint(_ context.Context, _, ticket string, cp hubclient.Checkpoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	cp.Ticket = ticket
	cp.Phase = cp.Data["PHASE"]
	f.store[ticket] = cp
	return nil
}

func (f *fakeHub) GetCheckpoint(_ context.Context, _, ticket string) (hubclient.Checkpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp, ok := f.store[ticket]
	return cp, ok, nil
}

func (f *fakeHub) DeleteCheckpoint(_ context.Context, _, ticket string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store, ticket)
	return nil
}

func (f *fakeHub) Checkpoints(_ context.Context, _ string) ([]hubclient.Checkpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]hubclient.Checkpoint, 0, len(f.store))
	for _, cp := range f.store {
		out = append(out, cp)
	}
	return out, nil
}

func TestSetHydratesThenWritesFullSet(t *testing.T) {
	hub := newFakeHub()
	hub.store["COD-1"] = hubclient.Checkpoint{Ticket: "COD-1", Phase: "built", Data: map[string]string{"PHASE": "built", "BRANCH": "feature/x"}}
	s := New(hub, "repo", time.Second)

	if err := s.Set("COD-1", "PHASE", "handed_off"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := hub.store["COD-1"].Data
	if got["PHASE"] != "handed_off" {
		t.Fatalf("PHASE = %q, want handed_off", got["PHASE"])
	}
	if got["BRANCH"] != "feature/x" {
		t.Fatalf("BRANCH = %q; hydrate must preserve fields the write did not touch", got["BRANCH"])
	}
	if got["UPDATED"] == "" {
		t.Fatalf("UPDATED not stamped on write")
	}
}

func TestGetReadsThroughCache(t *testing.T) {
	hub := newFakeHub()
	hub.store["COD-1"] = hubclient.Checkpoint{Ticket: "COD-1", Data: map[string]string{"PHASE": "built", "TITLE": "Do it"}}
	s := New(hub, "repo", time.Second)

	if got := s.Get("COD-1", "TITLE"); got != "Do it" {
		t.Fatalf("Get(TITLE) = %q, want Do it", got)
	}
	if got := s.Get("COD-1", "PHASE"); got != "built" {
		t.Fatalf("Get(PHASE) = %q, want built", got)
	}
	if got := s.Get("COD-1", "MISSING"); got != "" {
		t.Fatalf("Get(MISSING) = %q, want empty", got)
	}
}

func TestResumeTargetFuncFromHub(t *testing.T) {
	hub := newFakeHub()
	hub.store["COD-2"] = hubclient.Checkpoint{Ticket: "COD-2", Phase: state.Merged}
	hub.store["COD-3"] = hubclient.Checkpoint{Ticket: "COD-3", Phase: state.Verified}
	hub.store["COD-5"] = hubclient.Checkpoint{Ticket: "COD-5", Phase: state.Building}
	s := New(hub, "repo", time.Second)

	id, phase := s.ResumeTargetFunc(nil)
	if id != "COD-3" || phase != state.Verified {
		t.Fatalf("ResumeTargetFunc = %q/%q; want COD-3/verified (lowest in-flight, merged skipped)", id, phase)
	}

	id, _ = s.ResumeTargetFunc(func(t string) bool { return t == "COD-5" })
	if id != "COD-5" {
		t.Fatalf("filtered ResumeTargetFunc = %q; want COD-5", id)
	}
}

func TestRemoveStateDropsCheckpoint(t *testing.T) {
	hub := newFakeHub()
	s := New(hub, "repo", time.Second)
	if err := s.Set("COD-1", "PHASE", state.Building); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.RemoveState("COD-1"); err != nil {
		t.Fatalf("RemoveState: %v", err)
	}
	if _, ok := hub.store["COD-1"]; ok {
		t.Fatalf("checkpoint still on hub after RemoveState")
	}
}

// TestSetPausesWhenHubUnreachable drives a real client at a dead port so a write
// takes the true transport-failure path, then asserts the bounded retry exhausts
// into state.ErrHubUnreachable (ADR 0008 §3). A fake clock makes it deterministic.
func TestSetPausesWhenHubUnreachable(t *testing.T) {
	client := hubclient.New("http://127.0.0.1:1", "")
	clk := &fakeClock{t: time.Unix(0, 0)}
	s := New(client, "repo", 500*time.Millisecond)
	s.now = clk.now
	s.sleep = clk.sleep
	// Pre-hydrate so the write exercises the flush retry, not the read.
	s.cache["COD-1"] = map[string]string{"PHASE": state.Verified}
	s.hydrated["COD-1"] = true

	err := s.Set("COD-1", "PHASE", state.PROpen)

	if !errors.Is(err, state.ErrHubUnreachable) {
		t.Fatalf("Set against a dead hub = %v; want state.ErrHubUnreachable", err)
	}
	if len(clk.slept) == 0 {
		t.Fatalf("write did not retry before pausing")
	}
}

func TestHydrateUnreachableGetReturnsEmpty(t *testing.T) {
	client := hubclient.New("http://127.0.0.1:1", "")
	clk := &fakeClock{t: time.Unix(0, 0)}
	s := New(client, "repo", 300*time.Millisecond)
	s.now = clk.now
	s.sleep = clk.sleep

	if got := s.Get("COD-1", "PHASE"); got != "" {
		t.Fatalf("Get against a dead hub = %q; want empty", got)
	}
}

type fakeClock struct {
	t     time.Time
	slept []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
}
