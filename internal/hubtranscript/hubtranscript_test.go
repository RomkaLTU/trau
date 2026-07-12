package hubtranscript

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

type fakeHub struct {
	mu      sync.Mutex
	batches [][]hubclient.TranscriptChunk
}

func (f *fakeHub) AppendTranscript(_ context.Context, _ string, chunks []hubclient.TranscriptChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, append([]hubclient.TranscriptChunk(nil), chunks...))
	return nil
}

func (f *fakeHub) all() []hubclient.TranscriptChunk {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hubclient.TranscriptChunk
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

// TestSinkTeesChunksToHub checks a session writer turns each write into an ordered,
// base64-encoded chunk carrying the stem and dimensions, flushed to the hub.
func TestSinkTeesChunksToHub(t *testing.T) {
	hub := &fakeHub{}
	s := New(hub, "acme", 0, 0)
	w := s.Open("100-build", 80, 24)
	if _, err := w.Write([]byte("AAA")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := w.Write([]byte("BBB")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	s.Close()

	got := hub.all()
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].Stem != "100-build" || got[0].Seq != 0 || got[0].Cols != 80 || got[0].Rows != 24 || decode(t, got[0].Data) != "AAA" {
		t.Errorf("chunk 0 = %+v, want the first build slice", got[0])
	}
	if got[1].Seq != 1 || decode(t, got[1].Data) != "BBB" {
		t.Errorf("chunk 1 = %+v, want the second slice at seq 1", got[1])
	}
}

// TestSinkDropsOldestOverCap checks the byte cap drops the oldest queued chunks so
// the in-memory buffer can never grow without bound (ADR 0008 §3).
func TestSinkDropsOldestOverCap(t *testing.T) {
	s := newSink(&fakeHub{}, "acme", 200, time.Second)
	w := s.Open("100-build", 80, 24)
	for range 50 {
		if _, err := w.Write([]byte("0123456789")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bytes > s.maxBytes {
		t.Errorf("buffered %d bytes, want the cap %d enforced", s.bytes, s.maxBytes)
	}
	if len(s.buf) == 0 {
		t.Error("cap must keep the newest chunks, not drop everything")
	}
}

func decode(t *testing.T, b64 string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode %q: %v", b64, err)
	}
	return string(b)
}
