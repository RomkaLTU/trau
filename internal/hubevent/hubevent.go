// Package hubevent is the loop child's hub-backed event sink. It satisfies
// event.Sink by posting every emitted event to the serve hub's event API over
// HTTP (ADR 0008): the child stops appending a per-repo event-log file and sends
// events to the hub, which appends them to the authoritative events table and fans
// them out to live streams.
//
// Emit never blocks the loop. Events queue in a byte-bounded in-memory buffer that
// a background flusher drains, batching bursts into one POST and retrying an
// unreachable hub with backoff. A sustained hub outage surfaces as a blameless
// pause through the checkpoint writer, which shares the same hub (state.ErrHubUnreachable);
// the sink only has to hold events until the hub returns, dropping the oldest when
// the buffer fills first — lost feed activity reappears when the run reruns a phase.
package hubevent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/logger"
)

const (
	baseBackoff = 200 * time.Millisecond
	maxBackoff  = 2 * time.Second
	retryTick   = 2 * time.Second

	defaultBufferBytes = 32 << 20
	defaultRetryWindow = 30 * time.Second

	eventOverhead = 64
)

// hubAPI is the slice of hubclient the sink drives; *hubclient.Client satisfies it,
// and tests substitute a fake.
type hubAPI interface {
	AppendEvents(ctx context.Context, repo string, evs []hubclient.Event) error
}

// Sink is a hub-backed event.Sink scoped to one repo.
type Sink struct {
	client   hubAPI
	repo     string
	maxBytes int
	window   time.Duration

	now   func() time.Time
	sleep func(time.Duration)

	mu    sync.Mutex
	buf   []hubclient.Event
	bytes int

	notify chan struct{}
	done   chan struct{}
	closed chan struct{}
}

// New returns a Sink posting repo's events through client and starts its flusher.
// maxBytes caps the in-memory buffer (defaulted when non-positive); window bounds
// how long one flush retries an unreachable hub before backing off to the next
// cycle (defaulted when non-positive). Call Close to flush the tail before exit.
func New(client hubAPI, repo string, maxBytes int, window time.Duration) *Sink {
	s := newSink(client, repo, maxBytes, window)
	go s.run()
	return s
}

func newSink(client hubAPI, repo string, maxBytes int, window time.Duration) *Sink {
	if maxBytes <= 0 {
		maxBytes = defaultBufferBytes
	}
	if window <= 0 {
		window = defaultRetryWindow
	}
	return &Sink{
		client:   client,
		repo:     repo,
		maxBytes: maxBytes,
		window:   window,
		now:      time.Now,
		sleep:    time.Sleep,
		notify:   make(chan struct{}, 1),
		done:     make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

// Event queues ev for the hub and nudges the flusher. It never blocks; when the
// buffer is over its byte cap the oldest queued events are dropped to make room.
func (s *Sink) Event(ev event.Event) {
	e := clientEvent(ev)
	s.mu.Lock()
	s.buf = append(s.buf, e)
	s.bytes += eventBytes(e)
	s.enforceCap()
	s.mu.Unlock()
	s.wake()
}

// Close stops the flusher after a final attempt to flush the tail, so a run's
// terminal events reach the hub before the process exits. It is safe to call once.
func (s *Sink) Close() {
	close(s.done)
	<-s.closed
}

func (s *Sink) run() {
	defer close(s.closed)
	t := time.NewTicker(retryTick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			s.flush()
			return
		case <-s.notify:
			s.flush()
		case <-t.C:
			s.flush()
		}
	}
}

// flush sends the whole buffer to the hub in one ordered batch. On a lasting
// unreachable hub it puts the batch back at the front so the next cycle retries it
// ahead of anything newer, re-enforcing the cap.
func (s *Sink) flush() {
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.buf
	s.buf = nil
	s.bytes = 0
	s.mu.Unlock()

	if s.post(batch) {
		return
	}
	s.mu.Lock()
	s.buf = append(batch, s.buf...)
	s.bytes = totalBytes(s.buf)
	s.enforceCap()
	s.mu.Unlock()
}

// post flushes batch, retrying an unreachable hub with backoff until the window
// expires. It returns true when the batch is done with — flushed, or dropped
// because the hub rejected it (a non-connection error retrying cannot fix) — and
// false when the hub is still unreachable, so the caller keeps the batch.
func (s *Sink) post(batch []hubclient.Event) bool {
	deadline := s.now().Add(s.window)
	backoff := baseBackoff
	for {
		err := s.client.AppendEvents(context.Background(), s.repo, batch)
		if err == nil {
			return true
		}
		if !hubclient.IsUnreachable(err) {
			logger.Verbosef("append events %s: %v", s.repo, err)
			return true
		}
		if !s.now().Before(deadline) || s.stopping() {
			return false
		}
		s.sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

func (s *Sink) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Sink) stopping() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Sink) enforceCap() {
	for s.bytes > s.maxBytes && len(s.buf) > 0 {
		s.bytes -= eventBytes(s.buf[0])
		s.buf = s.buf[1:]
	}
}

func clientEvent(ev event.Event) hubclient.Event {
	return hubclient.Event{
		TS:     ev.Time,
		Kind:   ev.Kind,
		Phase:  ev.Phase,
		Msg:    ev.Msg,
		Fields: marshalFields(ev.Fields),
	}
}

func marshalFields(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

func eventBytes(e hubclient.Event) int {
	return len(e.TS) + len(e.Kind) + len(e.Phase) + len(e.Msg) + len(e.Fields) + eventOverhead
}

func totalBytes(evs []hubclient.Event) int {
	n := 0
	for _, e := range evs {
		n += eventBytes(e)
	}
	return n
}
