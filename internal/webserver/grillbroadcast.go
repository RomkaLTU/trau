package webserver

import (
	"slices"
	"sync"
)

// grillBroadcastBuffer is how many events a grill subscriber may fall behind
// before the broadcaster drops rather than block the writer. A single session's
// chat is low volume, so the buffer is modest; a subscriber that falls behind
// heals on its next reconnect backfill from the message store.
const grillBroadcastBuffer = 256

// grillActivityReplay is how many of the running turn's activity frames the
// broadcaster keeps per session so a stream opening mid-turn can be shown what the
// agent has been doing. Activity is never stored, so this in-memory buffer is the only
// backfill it has; it matches the panel's own ring (ACTIVITY_RING in
// web/src/lib/grill.ts) so a replay fills the client exactly.
const grillActivityReplay = 50

// liveGrillEvent is one grill session update delivered to live SSE subscribers.
// Event is the SSE event name ("message", "state", "delta" or "activity"); FrameID is
// the resume id a message frame carries (empty for every other frame, none of which
// is resumable); Payload is the JSON body.
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
	mu       sync.Mutex
	subs     map[int]chan liveGrillEvent
	next     int
	seq      map[int64]int
	activity map[int64]int
	recent   map[int64][]GrillActivityView
}

func newGrillBroadcaster() *grillBroadcaster {
	return &grillBroadcaster{
		subs:     map[int]chan liveGrillEvent{},
		seq:      map[int64]int{},
		activity: map[int64]int{},
		recent:   map[int64][]GrillActivityView{},
	}
}

func (b *grillBroadcaster) subscribe() (int, <-chan liveGrillEvent) {
	id, ch, _ := b.subscribeSession(0)
	return id, ch
}

// subscribeSession subscribes and snapshots sid's buffered activity under the one
// lock, so a frame published while a stream is opening reaches the new subscriber
// exactly once: in the replay or on the channel, never in both. Session ids start at
// one, so the plain subscriber passes zero and is handed nothing to replay.
func (b *grillBroadcaster) subscribeSession(sid int64) (int, <-chan liveGrillEvent, []GrillActivityView) {
	ch := make(chan liveGrillEvent, grillBroadcastBuffer)
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	b.subs[id] = ch
	return id, ch, slices.Clone(b.recent[sid])
}

func (b *grillBroadcaster) unsubscribe(id int) {
	b.mu.Lock()
	delete(b.subs, id)
	b.mu.Unlock()
}

// removeSession drops what the broadcaster holds for a session that is gone for good.
func (b *grillBroadcaster) removeSession(sid int64) {
	b.mu.Lock()
	delete(b.seq, sid)
	delete(b.activity, sid)
	delete(b.recent, sid)
	b.mu.Unlock()
}

// publish delivers ev to every live subscriber, skipping any whose buffer is full
// so a slow reader never stalls the write path. A delta is numbered here, under the
// lock that delivers it, so its seq and its position in the stream cannot disagree.
// Activity is interleaved with the reply and so counts on its own, leaving the reply
// unbroken, and is buffered as it is numbered so a stream that opens mid-turn can be
// caught up; a message or state frame ends the turn and restarts both counts and
// empties the buffer — the same boundary the panel clears its buffer on, since one
// child serves every turn of an interview.
func (b *grillBroadcaster) publish(ev liveGrillEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch payload := ev.Payload.(type) {
	case GrillDeltaView:
		b.seq[ev.SessionID]++
		payload.Seq = b.seq[ev.SessionID]
		ev.Payload = payload
	case GrillActivityView:
		b.activity[ev.SessionID]++
		payload.Seq = b.activity[ev.SessionID]
		ev.Payload = payload
		b.buffer(ev.SessionID, payload)
	default:
		delete(b.seq, ev.SessionID)
		delete(b.activity, ev.SessionID)
		delete(b.recent, ev.SessionID)
	}
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// buffer records one numbered activity frame for replay, dropping the oldest once the
// ring is full. The caller holds b.mu; the trim copies rather than reslices so the
// frames that aged out are released with it.
func (b *grillBroadcaster) buffer(sid int64, act GrillActivityView) {
	ring := append(b.recent[sid], act)
	if len(ring) > grillActivityReplay {
		ring = slices.Clone(ring[len(ring)-grillActivityReplay:])
	}
	b.recent[sid] = ring
}
