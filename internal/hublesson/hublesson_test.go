package hublesson

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
)

// fakeHub holds a repo's ledger append-order (oldest first) and serves it back
// most-recent first, the way the real hub does.
type fakeHub struct {
	lessons []hubclient.Lesson
}

func (f *fakeHub) AppendLesson(_ context.Context, _ string, l hubclient.Lesson) error {
	f.lessons = append(f.lessons, l)
	return nil
}

func (f *fakeHub) Lessons(_ context.Context, _ string) ([]hubclient.Lesson, error) {
	out := make([]hubclient.Lesson, len(f.lessons))
	for i, l := range f.lessons {
		out[len(f.lessons)-1-i] = l
	}
	return out, nil
}

func neverUnreachable(error) bool { return false }

func TestAppendAndAllReturnsAppendOrder(t *testing.T) {
	hub := &fakeHub{}
	s := New(hub, "repo", time.Second, neverUnreachable)

	if err := s.Append(hubclient.Lesson{Ticket: "COD-1", Lesson: "older"}); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	if err := s.Append(hubclient.Lesson{Ticket: "COD-2", Lesson: "newer"}); err != nil {
		t.Fatalf("Append second: %v", err)
	}

	got, err := s.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	// The hub serves newest-first; All reverses it to append order so the relevance
	// scan's recency tie-break matches the file era.
	if len(got) != 2 || got[0].Lesson != "older" || got[1].Lesson != "newer" {
		t.Fatalf("All = %+v, want append order [older, newer]", got)
	}
}

// TestAppendRetriesThenErrHubUnreachable drives a real client at a dead port so a
// write takes the transport-failure path, then asserts the bounded retry exhausts
// into state.ErrHubUnreachable (ADR 0008 §3). A fake clock makes it deterministic.
func TestAppendRetriesThenErrHubUnreachable(t *testing.T) {
	client := hubclient.New("http://127.0.0.1:1", "")
	clk := &fakeClock{t: time.Unix(0, 0)}
	s := New(client, "repo", 500*time.Millisecond, hubclient.IsUnreachable)
	s.now = clk.now
	s.sleep = clk.sleep

	err := s.Append(hubclient.Lesson{Ticket: "COD-1", Lesson: "x"})

	if !errors.Is(err, state.ErrHubUnreachable) {
		t.Fatalf("Append against a dead hub = %v; want state.ErrHubUnreachable", err)
	}
	if len(clk.slept) == 0 {
		t.Fatalf("write did not retry before giving up")
	}
}

func TestAllPropagatesNonConnectionError(t *testing.T) {
	s := New(&errHub{err: errors.New("boom")}, "repo", time.Second, neverUnreachable)
	if _, err := s.All(); err == nil {
		t.Fatalf("All should propagate a non-connection error")
	}
}

type errHub struct{ err error }

func (e *errHub) AppendLesson(context.Context, string, hubclient.Lesson) error { return e.err }
func (e *errHub) Lessons(context.Context, string) ([]hubclient.Lesson, error) {
	return nil, e.err
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
