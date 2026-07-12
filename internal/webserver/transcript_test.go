package webserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/transcriptdb"
)

// transcriptServer builds a hub over a real transcripts database with one
// registered repo, so the transcript handlers exercise the chunk store end to end.
func transcriptServer(t *testing.T) *httptest.Server {
	t.Helper()
	home := t.TempDir()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tdb, err := transcriptdb.Open(home)
	if err != nil {
		t.Fatalf("open transcript db: %v", err)
	}
	t.Cleanup(func() { _ = tdb.Close() })
	stores := hubstore.NewStores(db.SQL(), tdb.SQL(), 50)
	repo := registry.Repo{Name: "acme", Root: filepath.Join(home, "acme"), RunsDir: filepath.Join(home, "acme", ".trau", "runs")}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	ts := httptest.NewServer(New("1.2.3", "127.0.0.1", "", nil, false, stores).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func chunk(stem string, seq int64, cols, rows int, data string) hubclient.TranscriptChunk {
	return hubclient.TranscriptChunk{Stem: stem, Seq: seq, Cols: cols, Rows: rows, Data: base64.StdEncoding.EncodeToString([]byte(data))}
}

func postChunks(t *testing.T, ts *httptest.Server, repo string, chunks ...hubclient.TranscriptChunk) {
	t.Helper()
	body, err := json.Marshal(struct {
		Chunks []hubclient.TranscriptChunk `json:"chunks"`
	}{Chunks: chunks})
	if err != nil {
		t.Fatalf("marshal chunks: %v", err)
	}
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/transcripts", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST transcripts: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST transcripts status = %d, want 200", res.StatusCode)
	}
}

// TestTranscriptsListFromStore checks a posted session surfaces in the list with
// its label, dimensions, byte size, and the newest marked live.
func TestTranscriptsListFromStore(t *testing.T) {
	ts := transcriptServer(t)
	postChunks(t, ts, "acme", chunk("100-build", 0, 80, 24, "hello"))

	_, body := get(t, ts, APIPrefix+"/repos/acme/transcripts")
	var out TranscriptsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(out.Transcripts) != 1 {
		t.Fatalf("got %d transcripts, want 1", len(out.Transcripts))
	}
	v := out.Transcripts[0]
	if v.ID != "100-build" || v.Label != "build" || v.Cols != 80 || v.Rows != 24 || v.Size != 5 || !v.Live {
		t.Errorf("view = %+v, want the build session sized 80x24, 5 bytes, live", v)
	}
}

// TestTranscriptChunksPoll checks the poll serves a pinned session from the cursor
// and that follow resolves the newest session.
func TestTranscriptChunksPoll(t *testing.T) {
	ts := transcriptServer(t)
	postChunks(t, ts, "acme", chunk("100-build", 0, 80, 24, "AAA"), chunk("100-build", 1, 80, 24, "BBB"))

	from := pollChunks(t, ts, "?id=100-build&after=-1")
	if from.ID != "100-build" || from.Cols != 80 || from.Rows != 24 {
		t.Fatalf("poll meta = %+v, want the build session 80x24", from)
	}
	if len(from.Chunks) != 2 || decode(t, from.Chunks[0].Data) != "AAA" || decode(t, from.Chunks[1].Data) != "BBB" {
		t.Fatalf("poll from top = %+v, want AAA then BBB", from.Chunks)
	}

	after := pollChunks(t, ts, "?id=100-build&after=0")
	if len(after.Chunks) != 1 || decode(t, after.Chunks[0].Data) != "BBB" {
		t.Fatalf("poll after seq 0 = %+v, want only BBB", after.Chunks)
	}

	postChunks(t, ts, "acme", chunk("200-verify", 0, 100, 40, "V"))
	follow := pollChunks(t, ts, "?follow=1&after=-1")
	if follow.ID != "200-verify" || len(follow.Chunks) != 1 || decode(t, follow.Chunks[0].Data) != "V" {
		t.Fatalf("follow poll = %+v, want the newest verify session", follow)
	}
}

// TestTranscriptChunksClientCursor checks the Go client's poll pages strictly past
// the seq it last saw — a cursor of 0 must not replay seq 0 — which is what keeps
// the TUI and watch live views from re-writing the first chunk each tick.
func TestTranscriptChunksClientCursor(t *testing.T) {
	ts := transcriptServer(t)
	postChunks(t, ts, "acme", chunk("100-build", 0, 80, 24, "AAA"), chunk("100-build", 1, 80, 24, "BBB"))

	hub := hubclient.New(ts.URL, "")
	poll, err := hub.TranscriptChunks(context.Background(), "acme", "100-build", 0, false, 0)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if string(poll.Data) != "BBB" || poll.Seq != 1 {
		t.Fatalf("poll after seq 0 = %q at seq %d, want only BBB at seq 1", poll.Data, poll.Seq)
	}
}

// TestTranscriptStreamBackfills checks the SSE stream replays a stored session's
// meta and chunk frames from the store.
func TestTranscriptStreamBackfills(t *testing.T) {
	ts := transcriptServer(t)
	postChunks(t, ts, "acme", chunk("100-build", 0, 80, 24, "hello world"))

	r := openSSE(t, ts, APIPrefix+"/repos/acme/transcript/stream?id=100-build", nil)
	if meta := nextMetaFrame(t, r); meta.ID != "100-build" || meta.Cols != 80 || meta.Rows != 24 {
		t.Fatalf("meta frame = %+v, want the build session 80x24", meta)
	}
	if got := nextChunkFrame(t, r); got != "hello world" {
		t.Fatalf("chunk frame = %q, want the stored bytes", got)
	}
}

func pollChunks(t *testing.T, ts *httptest.Server, query string) transcriptChunksResponse {
	t.Helper()
	_, body := get(t, ts, APIPrefix+"/repos/acme/transcript/chunks"+query)
	var out transcriptChunksResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	return out
}

func decode(t *testing.T, b64 string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode chunk %q: %v", b64, err)
	}
	return string(b)
}

func nextMetaFrame(t *testing.T, r *bufio.Reader) transcriptMeta {
	t.Helper()
	event, data := readFrame(t, r)
	if event != "meta" {
		t.Fatalf("first frame event = %q, want meta", event)
	}
	var m transcriptMeta
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("decode meta %q: %v", data, err)
	}
	return m
}

func nextChunkFrame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	_, data := readFrame(t, r)
	return decode(t, data)
}

// readFrame reads one SSE frame, returning its event type (empty for a plain data
// frame) and the data payload, skipping keepalive comments.
func readFrame(t *testing.T, r *bufio.Reader) (event, data string) {
	t.Helper()
	for {
		event, data = "", ""
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read frame: %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			switch {
			case strings.HasPrefix(line, "event:"):
				event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(line[len("data:"):])
			}
		}
		if data != "" {
			return event, data
		}
	}
}
