package webserver

import "sync"

// transcriptBroadcastBuffer is how many chunks a subscriber may fall behind before
// the broadcaster drops rather than block the appender. Transcript bursts are
// larger than the event feed's, so the buffer is deeper; a subscriber that still
// falls behind heals on its next reconnect backfill from the chunk store.
const transcriptBroadcastBuffer = 1024

// liveTranscriptChunk is one appended transcript chunk delivered to live SSE
// subscribers. Root is the repo key subscribers filter on; Data is the raw
// terminal bytes.
type liveTranscriptChunk struct {
	Root string
	Stem string
	Seq  int64
	Cols int
	Rows int
	Data []byte
}

// transcriptBroadcaster fans appended transcript chunks out to live SSE
// subscribers in-process, so the live tail never reads the database (ADR 0008 §4).
// The hub is the sole appender, so one broadcaster feeds every stream: each
// subscriber gets a buffered channel and one that falls behind drops rather than
// blocking the append.
type transcriptBroadcaster struct {
	mu   sync.Mutex
	subs map[int]chan liveTranscriptChunk
	next int
}

func newTranscriptBroadcaster() *transcriptBroadcaster {
	return &transcriptBroadcaster{subs: map[int]chan liveTranscriptChunk{}}
}

func (b *transcriptBroadcaster) subscribe() (int, <-chan liveTranscriptChunk) {
	ch := make(chan liveTranscriptChunk, transcriptBroadcastBuffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return id, ch
}

func (b *transcriptBroadcaster) unsubscribe(id int) {
	b.mu.Lock()
	delete(b.subs, id)
	b.mu.Unlock()
}

// publish delivers c to every live subscriber, skipping any whose buffer is full so
// a slow reader never stalls the append path.
func (b *transcriptBroadcaster) publish(c liveTranscriptChunk) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- c:
		default:
		}
	}
}
