// Package hubtranscript is the loop child's hub-backed transcript sink. The agent
// no longer writes a per-phase .pty.log file (ADR 0008 §4); it opens a writer here
// per session and the raw PTY bytes flow child → hub as ordered chunks, which the
// hub persists to transcripts.db and fans out to the live tail.
//
// Writes never block the agent. Chunks queue in a byte-bounded in-memory buffer a
// background flusher drains, batching bursts into one POST and retrying an
// unreachable hub with backoff. Transcripts are the least-authoritative run data:
// when the buffer fills or the hub stays down, the oldest chunks are dropped
// rather than pausing the run (a downed hub pauses only through the checkpoint
// writer). Lost tail bytes simply do not replay.
package hubtranscript

import (
	"context"
	"encoding/base64"
	"io"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/logger"
)

const (
	baseBackoff = 200 * time.Millisecond
	maxBackoff  = 2 * time.Second
	retryTick   = 2 * time.Second

	defaultBufferBytes = 32 << 20
	defaultRetryWindow = 30 * time.Second

	chunkOverhead = 48
)

// hubAPI is the slice of hubclient the sink drives; *hubclient.Client satisfies it,
// and tests substitute a fake.
type hubAPI interface {
	AppendTranscript(ctx context.Context, repo string, chunks []hubclient.TranscriptChunk) error
}

// Sink posts a repo's transcript chunks through client. It satisfies the agent's
// transcript sink: Open returns a per-session writer the agent tees PTY output to.
type Sink struct {
	client   hubAPI
	repo     string
	maxBytes int
	window   time.Duration

	now   func() time.Time
	sleep func(time.Duration)

	mu    sync.Mutex
	buf   []hubclient.TranscriptChunk
	bytes int

	notify chan struct{}
	done   chan struct{}
	closed chan struct{}
}

// New returns a Sink posting repo's transcript chunks through client and starts its
// flusher. maxBytes caps the in-memory buffer (defaulted when non-positive); window
// bounds how long one flush retries an unreachable hub before backing off. Call
// Close to flush the tail before exit.
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

// Open returns a writer capturing one agent session's PTY output under stem, sized
// cols×rows. The agent writes raw bytes; each write becomes an ordered chunk.
func (s *Sink) Open(stem string, cols, rows int) io.WriteCloser {
	return &writer{sink: s, stem: stem, cols: cols, rows: rows}
}

// Close stops the flusher after a final attempt to flush the tail. Safe to call once.
func (s *Sink) Close() {
	close(s.done)
	<-s.closed
}

type writer struct {
	sink *Sink
	stem string
	cols int
	rows int
	seq  int64
}

// Write queues p as the next chunk of this session and never blocks the agent.
func (w *writer) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.sink.enqueue(hubclient.TranscriptChunk{
		Stem: w.stem,
		Seq:  w.seq,
		Cols: w.cols,
		Rows: w.rows,
		Data: base64.StdEncoding.EncodeToString(p),
	})
	w.seq++
	return len(p), nil
}

func (w *writer) Close() error { return nil }

func (s *Sink) enqueue(c hubclient.TranscriptChunk) {
	s.mu.Lock()
	s.buf = append(s.buf, c)
	s.bytes += chunkBytes(c)
	s.enforceCap()
	s.mu.Unlock()
	s.wake()
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
// ahead of anything newer, re-enforcing the cap so the oldest chunks drop.
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
// because the hub rejected it — and false when the hub is still unreachable, so
// the caller keeps the batch for the next cycle.
func (s *Sink) post(batch []hubclient.TranscriptChunk) bool {
	deadline := s.now().Add(s.window)
	backoff := baseBackoff
	for {
		err := s.client.AppendTranscript(context.Background(), s.repo, batch)
		if err == nil {
			return true
		}
		if !hubclient.IsUnreachable(err) {
			logger.Verbosef("append transcript %s: %v", s.repo, err)
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
		s.bytes -= chunkBytes(s.buf[0])
		s.buf = s.buf[1:]
	}
}

func chunkBytes(c hubclient.TranscriptChunk) int {
	return len(c.Data) + len(c.Stem) + chunkOverhead
}

func totalBytes(chunks []hubclient.TranscriptChunk) int {
	n := 0
	for _, c := range chunks {
		n += chunkBytes(c)
	}
	return n
}
