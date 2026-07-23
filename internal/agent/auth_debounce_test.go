package agent

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeAuthTimer struct {
	delay    time.Duration
	callback func()
	stopped  bool
}

func (t *fakeAuthTimer) Stop() bool {
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (t *fakeAuthTimer) fire() {
	if !t.stopped {
		t.callback()
	}
}

type fakeAuthTimerFactory struct {
	timers []*fakeAuthTimer
}

func (f *fakeAuthTimerFactory) newTimer(delay time.Duration, callback func()) authTimer {
	timer := &fakeAuthTimer{delay: delay, callback: callback}
	f.timers = append(f.timers, timer)
	return timer
}

type chunkReader struct {
	chunks [][]byte
}

func newChunkReader(chunks ...string) *chunkReader {
	reader := &chunkReader{chunks: make([][]byte, 0, len(chunks))}
	for _, chunk := range chunks {
		reader.chunks = append(reader.chunks, []byte(chunk))
	}
	return reader
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks[0] = r.chunks[0][n:]
	if len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestDrainAuthQuoteThenStreamingDoesNotSignal(t *testing.T) {
	quoted := `Reviewing the "please run /login" matcher in prose.` + "\n"
	streaming := strings.Repeat("working output\n", 1600)
	src := newChunkReader(quoted, streaming)
	authPrompt := make(chan struct{}, 1)
	var transcript bytes.Buffer

	drainWithTrustSignal(&transcript, src, make(chan struct{}, 1), authPrompt, nil)

	select {
	case <-authPrompt:
		t.Fatal("authPrompt fired for quoted login text followed by continued output")
	default:
	}
	if got, want := transcript.String(), quoted+streaming; got != want {
		t.Fatalf("transcript mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestAuthDebouncerWallThenSilence(t *testing.T) {
	authPrompt := make(chan struct{}, 1)
	factory := &fakeAuthTimerFactory{}
	debouncer := newAuthDebouncer(authPrompt, authQuietWindow, factory.newTimer)

	debouncer.arm()

	select {
	case <-authPrompt:
		t.Fatal("authPrompt fired before the quiet window")
	default:
	}
	if len(factory.timers) != 1 {
		t.Fatalf("timers = %d, want 1", len(factory.timers))
	}
	if factory.timers[0].delay != authQuietWindow {
		t.Fatalf("timer delay = %s, want %s", factory.timers[0].delay, authQuietWindow)
	}

	factory.timers[0].fire()
	select {
	case <-authPrompt:
	default:
		t.Fatal("authPrompt did not fire after the quiet window")
	}
}

func TestDrainAuthWallThenEOFSignals(t *testing.T) {
	authPrompt := make(chan struct{}, 1)
	drainWithTrustSignal(
		io.Discard,
		strings.NewReader("API Error: 403 Request not allowed · Please run /login\n"),
		make(chan struct{}, 1),
		authPrompt,
		nil,
	)

	select {
	case <-authPrompt:
	default:
		t.Fatal("authPrompt did not fire before the drain returned")
	}
}

func TestAuthDebouncerRerenderRearms(t *testing.T) {
	authPrompt := make(chan struct{}, 1)
	factory := &fakeAuthTimerFactory{}
	debouncer := newAuthDebouncer(authPrompt, authQuietWindow, factory.newTimer)

	debouncer.arm()
	debouncer.observeOutput(8 << 10)
	debouncer.arm()

	if len(factory.timers) != 2 {
		t.Fatalf("timers = %d, want 2", len(factory.timers))
	}
	if !factory.timers[0].stopped {
		t.Fatal("first quiet timer remained active after the wall re-rendered")
	}
	factory.timers[0].fire()
	select {
	case <-authPrompt:
		t.Fatal("stale quiet timer fired authPrompt")
	default:
	}

	factory.timers[1].fire()
	select {
	case <-authPrompt:
	default:
		t.Fatal("re-armed quiet timer did not fire authPrompt")
	}
}
