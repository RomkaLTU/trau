package hubphaselog

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
	store map[string][]hubclient.PhaseLog
}

func newFakeHub() *fakeHub { return &fakeHub{store: map[string][]hubclient.PhaseLog{}} }

func (f *fakeHub) PutPhaseLog(_ context.Context, _, ticket, phase, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	logs := f.store[ticket]
	for i, l := range logs {
		if l.Phase == phase {
			logs[i].Content = content
			return nil
		}
	}
	// Prepend so the newest write reads back first, like the hub's ordering.
	f.store[ticket] = append([]hubclient.PhaseLog{{Phase: phase, Content: content}}, logs...)
	return nil
}

func (f *fakeHub) PhaseLogs(_ context.Context, _, ticket string) ([]hubclient.PhaseLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.store[ticket], nil
}

func (f *fakeHub) DeletePhaseLogs(_ context.Context, _, ticket string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store, ticket)
	return nil
}

func neverUnreachable(error) bool { return false }

func TestPutListRoundTrip(t *testing.T) {
	hub := newFakeHub()
	s := New(hub, "repo", time.Second, neverUnreachable)

	if err := s.Put("COD-1", "build", "build output"); err != nil {
		t.Fatalf("Put build: %v", err)
	}
	if err := s.Put("COD-1", "verify", "verify output"); err != nil {
		t.Fatalf("Put verify: %v", err)
	}
	logs, err := s.List("COD-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(logs) != 2 || logs[0].Phase != "verify" || logs[0].Content != "verify output" {
		t.Fatalf("List = %+v, want verify newest-first", logs)
	}
}

func TestRemoveDropsTicket(t *testing.T) {
	hub := newFakeHub()
	s := New(hub, "repo", time.Second, neverUnreachable)
	_ = s.Put("COD-1", "build", "b")
	if err := s.Remove("COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if logs, _ := s.List("COD-1"); len(logs) != 0 {
		t.Fatalf("phase logs survived Remove: %+v", logs)
	}
}

// TestPutRetriesThenErrHubUnreachable drives a real client at a dead port so a
// write takes the true transport-failure path, then asserts the bounded retry
// exhausts into state.ErrHubUnreachable (ADR 0008 §3). A fake clock makes it
// deterministic.
func TestPutRetriesThenErrHubUnreachable(t *testing.T) {
	client := hubclient.New("http://127.0.0.1:1", "")
	clk := &fakeClock{t: time.Unix(0, 0)}
	s := New(client, "repo", 500*time.Millisecond, hubclient.IsUnreachable)
	s.now = clk.now
	s.sleep = clk.sleep

	err := s.Put("COD-1", "build", "output")

	if !errors.Is(err, state.ErrHubUnreachable) {
		t.Fatalf("Put against a dead hub = %v; want state.ErrHubUnreachable", err)
	}
	if len(clk.slept) == 0 {
		t.Fatalf("write did not retry before giving up")
	}
}

func TestListPropagatesNonConnectionError(t *testing.T) {
	s := New(&errHub{err: errors.New("boom")}, "repo", time.Second, neverUnreachable)
	if _, err := s.List("COD-1"); err == nil {
		t.Fatalf("List should propagate a non-connection error")
	}
}

type errHub struct{ err error }

func (e *errHub) PutPhaseLog(context.Context, string, string, string, string) error { return e.err }
func (e *errHub) PhaseLogs(context.Context, string, string) ([]hubclient.PhaseLog, error) {
	return nil, e.err
}
func (e *errHub) DeletePhaseLogs(context.Context, string, string) error { return e.err }

type fakeClock struct {
	t     time.Time
	slept []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
}
