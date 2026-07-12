package webserver

import "sync"

// broadcastBuffer is how many events a subscriber may fall behind before the
// broadcaster drops rather than block the appender. A local feed is low volume, so
// a lagging subscriber is rare; when it happens the gap heals on the client's next
// reconnect backfill from the table.
const broadcastBuffer = 256

// liveEvent is one appended event delivered to live SSE subscribers. Root is the
// store key the per-repo stream filters on; Name tags the frame for the
// machine-wide stream, matching the identity the web UI addresses a repo by.
type liveEvent struct {
	Root  string
	Name  string
	Event FeedEvent
}

// eventBroadcaster fans appended events out to live SSE subscribers in-process.
// The hub is the sole appender (ADR 0008), so one broadcaster feeds every stream:
// each subscriber gets a buffered channel, and a subscriber that falls behind
// drops events rather than blocking the append.
type eventBroadcaster struct {
	mu   sync.Mutex
	subs map[int]chan liveEvent
	next int
}

func newEventBroadcaster() *eventBroadcaster {
	return &eventBroadcaster{subs: map[int]chan liveEvent{}}
}

// subscribe registers a live subscriber and returns its id and channel. The caller
// must unsubscribe when it disconnects.
func (b *eventBroadcaster) subscribe() (int, <-chan liveEvent) {
	ch := make(chan liveEvent, broadcastBuffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return id, ch
}

func (b *eventBroadcaster) unsubscribe(id int) {
	b.mu.Lock()
	delete(b.subs, id)
	b.mu.Unlock()
}

// publish delivers ev to every live subscriber, skipping any whose buffer is full
// so a slow reader never stalls the append path.
func (b *eventBroadcaster) publish(ev liveEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
