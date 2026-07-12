package hubevent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
)

// fakeHub records the batches it receives and can fail a number of leading calls
// with a genuine unreachable error so the retry path is exercised.
type fakeHub struct {
	mu      sync.Mutex
	batches [][]hubclient.Event
	fails   int
	unreach error
}

func (f *fakeHub) AppendEvents(_ context.Context, _ string, evs []hubclient.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return f.unreach
	}
	f.batches = append(f.batches, append([]hubclient.Event(nil), evs...))
	return nil
}

func (f *fakeHub) received() []hubclient.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hubclient.Event
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

// unreachableErr produces a real hubclient transport error by dialing a dead
// port, so IsUnreachable recognizes it — the same trick hubcheckpoint's test uses.
func unreachableErr(t *testing.T) error {
	t.Helper()
	err := hubclient.New("http://127.0.0.1:1", "").AppendEvents(context.Background(), "repo", nil)
	if err == nil || !hubclient.IsUnreachable(err) {
		t.Fatalf("expected an unreachable error, got %v", err)
	}
	return err
}

func kindMsg(kind, msg string) event.Event { return event.Event{Kind: kind, Msg: msg} }

func msgs(evs []hubclient.Event) string {
	s := ""
	for _, e := range evs {
		s += e.Msg
	}
	return s
}

func TestSinkBatchesInOrder(t *testing.T) {
	fake := &fakeHub{}
	s := newSink(fake, "repo", 0, 0)

	s.Event(kindMsg("agent_call", "a"))
	s.Event(kindMsg("agent_call", "b"))
	s.flush()
	s.Event(kindMsg("state_change", "c"))
	s.flush()

	if got := msgs(fake.received()); got != "abc" {
		t.Fatalf("received %q, want abc in order", got)
	}
	if len(fake.batches) != 2 {
		t.Fatalf("batches = %d, want 2 (one per flush)", len(fake.batches))
	}
}

func TestSinkRetriesUnreachableThenFlushes(t *testing.T) {
	fake := &fakeHub{fails: 2, unreach: unreachableErr(t)}
	s := newSink(fake, "repo", 0, time.Second)
	s.sleep = func(time.Duration) {} // no real backoff wait

	s.Event(kindMsg("agent_call", "a"))
	s.flush()

	if got := msgs(fake.received()); got != "a" {
		t.Fatalf("received %q, want a after the hub recovered", got)
	}
}

func TestSinkKeepsBatchWhenHubStaysDown(t *testing.T) {
	fake := &fakeHub{fails: 1 << 20, unreach: unreachableErr(t)}
	s := newSink(fake, "repo", 0, 30*time.Second)
	s.sleep = func(time.Duration) {}
	base := time.Now()
	calls := 0
	s.now = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(time.Minute) // past the retry deadline
	}

	s.Event(kindMsg("agent_call", "a"))
	s.flush()

	if got := fake.received(); len(got) != 0 {
		t.Fatalf("received %d events, want 0 while the hub is down", len(got))
	}
	s.mu.Lock()
	buffered := len(s.buf)
	s.mu.Unlock()
	if buffered != 1 {
		t.Fatalf("buffered = %d, want 1 (batch kept for a later retry)", buffered)
	}
}

func TestSinkDropsOldestOverCap(t *testing.T) {
	fake := &fakeHub{}
	// Each event is eventOverhead + 1 (kind) + 1 (msg) = 66 bytes; a 150-byte cap
	// holds two and drops the oldest when the third arrives.
	s := newSink(fake, "repo", 150, 0)

	s.Event(kindMsg("k", "1"))
	s.Event(kindMsg("k", "2"))
	s.Event(kindMsg("k", "3"))
	s.flush()

	if got := msgs(fake.received()); got != "23" {
		t.Fatalf("received %q, want 23 (oldest dropped over cap)", got)
	}
}

func TestSinkCloseFlushesTail(t *testing.T) {
	fake := &fakeHub{}
	s := New(fake, "repo", 0, 0)
	s.Event(kindMsg("state_change", "merged"))
	s.Close()

	if got := msgs(fake.received()); got != "merged" {
		t.Fatalf("received %q, want the terminal event flushed on Close", got)
	}
}

func TestSinkMarshalsFields(t *testing.T) {
	fake := &fakeHub{}
	s := newSink(fake, "repo", 0, 0)
	s.Event(event.Event{Kind: "build_no_skills", Fields: map[string]any{"ticket": "COD-1"}})
	s.flush()

	got := fake.received()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Fields != `{"ticket":"COD-1"}` {
		t.Fatalf("fields = %q, want the JSON object string", got[0].Fields)
	}
}
