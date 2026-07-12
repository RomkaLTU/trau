package hubartifact

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
)

type fakeHub struct {
	mu    sync.Mutex
	store map[string]string
	puts  int
}

func newFakeHub() *fakeHub { return &fakeHub{store: map[string]string{}} }

func key(ticket, kind string) string { return ticket + "/" + kind }

func (f *fakeHub) PutArtifact(_ context.Context, _, ticket, kind, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	f.store[key(ticket, kind)] = content
	return nil
}

func (f *fakeHub) GetArtifact(_ context.Context, _, ticket, kind string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.store[key(ticket, kind)]
	return c, ok, nil
}

func (f *fakeHub) DeleteArtifacts(_ context.Context, _, ticket string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.store {
		if strings.HasPrefix(k, ticket+"/") {
			delete(f.store, k)
		}
	}
	return nil
}

func neverUnreachable(error) bool { return false }

func TestPutGetRoundTrip(t *testing.T) {
	hub := newFakeHub()
	s := New(hub, "repo", time.Second, neverUnreachable)

	if err := s.Put("COD-1", "handoff", "the brief"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	content, ok, err := s.Get("COD-1", "handoff")
	if err != nil || !ok || content != "the brief" {
		t.Fatalf("Get = %q ok=%v err=%v", content, ok, err)
	}
	if _, ok, _ := s.Get("COD-1", "rubric"); ok {
		t.Fatalf("Get(absent) reported present")
	}
}

func TestRemoveDropsTicket(t *testing.T) {
	hub := newFakeHub()
	s := New(hub, "repo", time.Second, neverUnreachable)
	_ = s.Put("COD-1", "handoff", "b")
	_ = s.Put("COD-1", "rubric", "r")
	if err := s.Remove("COD-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := s.Get("COD-1", "handoff"); ok {
		t.Fatalf("artifact survived Remove")
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

	err := s.Put("COD-1", "handoff", "the brief")

	if !errors.Is(err, state.ErrHubUnreachable) {
		t.Fatalf("Put against a dead hub = %v; want state.ErrHubUnreachable", err)
	}
	if len(clk.slept) == 0 {
		t.Fatalf("write did not retry before giving up")
	}
}

func TestGetPropagatesNonConnectionError(t *testing.T) {
	hub := &errHub{err: errors.New("boom")}
	s := New(hub, "repo", time.Second, neverUnreachable)
	if _, _, err := s.Get("COD-1", "handoff"); err == nil {
		t.Fatalf("Get should propagate a non-connection error")
	}
}

type errHub struct{ err error }

func (e *errHub) PutArtifact(context.Context, string, string, string, string) error { return e.err }
func (e *errHub) GetArtifact(context.Context, string, string, string) (string, bool, error) {
	return "", false, e.err
}
func (e *errHub) DeleteArtifacts(context.Context, string, string) error { return e.err }

type fakeClock struct {
	t     time.Time
	slept []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
}
