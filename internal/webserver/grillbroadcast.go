package webserver

import "sync"

// grillBroadcastBuffer is how many events a grill subscriber may fall behind
// before the broadcaster drops rather than block the writer. A single session's
// chat is low volume, so the buffer is modest; a subscriber that falls behind
// heals on its next reconnect backfill from the message store.
const grillBroadcastBuffer = 256

// liveGrillEvent is one grill session update delivered to live SSE subscribers.
// Event is the SSE event name ("message" or "state"); FrameID is the resume id a
// message frame carries (empty for a state frame); Payload is the JSON body.
type liveGrillEvent struct {
	SessionID int64
	Event     string
	FrameID   string
	Payload   any
}

// grillBroadcaster fans grill message and state-change events out to live SSE
// subscribers in-process. The hub is the sole writer (ADR 0008), so one
// broadcaster feeds every stream: each subscriber gets a buffered channel and one
// that falls behind drops rather than blocking the write path.
type grillBroadcaster struct {
	mu   sync.Mutex
	subs map[int]chan liveGrillEvent
	next int
}

func newGrillBroadcaster() *grillBroadcaster {
	return &grillBroadcaster{subs: map[int]chan liveGrillEvent{}}
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
// so a slow reader never stalls the write path.
func (b *grillBroadcaster) publish(ev liveGrillEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
