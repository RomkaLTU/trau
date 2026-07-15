package webserver

import "sync"

// grillBroadcastBuffer is how many events a grill subscriber may fall behind
// before the broadcaster drops rather than block the writer. A single session's
// chat is low volume, so the buffer is modest; a subscriber that falls behind
// heals on its next reconnect backfill from the message store.
const grillBroadcastBuffer = 256

// liveGrillEvent is one grill session update delivered to live SSE subscribers.
// Event is the SSE event name ("message", "state" or "delta"); FrameID is the resume
// id a message frame carries (empty for a state or delta frame); Payload is the JSON
// body.
type liveGrillEvent struct {
	SessionID int64
	Event     string
	FrameID   string
	Payload   any
}

// grillBroadcaster fans grill message and state-change events out to live SSE
// subscribers in-process. The hub is the sole writer (ADR 0008), so one
// broadcaster feeds every stream: each subscriber gets a buffered channel and one
// that falls behind drops rather than blocking the write path. It also numbers
// each turn's deltas, which is what lets a client spot one that was dropped.
type grillBroadcaster struct {
	mu   sync.Mutex
	subs map[int]chan liveGrillEvent
	next int
	seq  map[int64]int
}

func newGrillBroadcaster() *grillBroadcaster {
	return &grillBroadcaster{subs: map[int]chan liveGrillEvent{}, seq: map[int64]int{}}
}

func (b *grillBroadcaster) subscribe() (int, <-chan liveGrillEvent) {
	ch := make(chan liveGrillEvent, grillBroadcastBuffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return id, ch
}

func (b *grillBroadcaster) unsubscribe(id int) {
	b.mu.Lock()
	delete(b.subs, id)
	b.mu.Unlock()
}

// publish delivers ev to every live subscriber, skipping any whose buffer is full
// so a slow reader never stalls the write path. A delta is numbered here, under the
// lock that delivers it, so its seq and its position in the stream cannot disagree.
// Every other frame ends the turn and restarts the count — the same boundary the
// panel clears its buffer on, since one child serves every turn of an interview.
func (b *grillBroadcaster) publish(ev liveGrillEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if delta, ok := ev.Payload.(GrillDeltaView); ok {
		b.seq[ev.SessionID]++
		delta.Seq = b.seq[ev.SessionID]
		ev.Payload = delta
	} else {
		delete(b.seq, ev.SessionID)
	}
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
