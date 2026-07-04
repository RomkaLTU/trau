package webserver

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
)

// writeTranscript writes a phase transcript and its dimensions sidecar under a
// repo's agent-results directory, exactly as a running loop's agent does — so the
// stream is exercised against real on-disk files, the "loop the hub did not
// start" case. It returns the transcript path so a test can append to it live.
func writeTranscript(t *testing.T, runsDir, stem string, cols, rows int, content string) string {
	t.Helper()
	dir := resultsDir(runsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}
	path := filepath.Join(dir, stem+agent.TranscriptExt)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.WriteFile(path+agent.SizeExt, []byte(fmt.Sprintf("%d %d\n", cols, rows)), 0o644); err != nil {
		t.Fatalf("write size: %v", err)
	}
	return path
}

// appendTranscript appends raw bytes to a transcript, as a phase does while the
// agent keeps producing output mid-request.
func appendTranscript(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatalf("append transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}
}

func setMtime(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

type ttFrame struct {
	event string
	id    string
	data  string
}

// nextTFrame reads the next non-keepalive SSE frame, capturing its event type,
// id, and data lines.
func nextTFrame(t *testing.T, r *bufio.Reader) ttFrame {
	t.Helper()
	for {
		var f ttFrame
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
				f.event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "id:"):
				f.id = strings.TrimSpace(line[len("id:"):])
			case strings.HasPrefix(line, "data:"):
				f.data = strings.TrimSpace(line[len("data:"):])
			}
		}
		if f.event == "" && f.id == "" && f.data == "" {
			continue
		}
		return f
	}
}

func (f ttFrame) meta(t *testing.T) transcriptMeta {
	t.Helper()
	if f.event != "meta" {
		t.Fatalf("frame event = %q, want meta", f.event)
	}
	var m transcriptMeta
	if err := json.Unmarshal([]byte(f.data), &m); err != nil {
		t.Fatalf("decode meta %q: %v", f.data, err)
	}
	return m
}

func (f ttFrame) chunk(t *testing.T) string {
	t.Helper()
	if f.event != "" {
		t.Fatalf("frame event = %q, want a chunk", f.event)
	}
	b, err := base64.StdEncoding.DecodeString(f.data)
	if err != nil {
		t.Fatalf("decode chunk %q: %v", f.data, err)
	}
	return string(b)
}

func openTranscriptStream(t *testing.T, ts *httptest.Server, repo, query string, header http.Header) *bufio.Reader {
	t.Helper()
	path := APIPrefix + "/repos/" + repo + "/transcript/stream"
	if query != "" {
		path += "?" + query
	}
	return openSSE(t, ts, path, header)
}

// TestTranscriptStreamReplaysThenTailsAppends is the streaming contract test: a
// fresh connection sizes the terminal from the recorded dimensions, replays the
// existing transcript, then delivers bytes appended mid-request as the next chunk.
func TestTranscriptStreamReplaysThenTailsAppends(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	writeTranscript(t, runsDir, "100-build", 120, 40, "hello")

	ts := instancesServer(t, home)
	r := openTranscriptStream(t, ts, "acme", "", nil)

	meta := nextTFrame(t, r).meta(t)
	if meta.ID != "100-build" || meta.Cols != 120 || meta.Rows != 40 {
		t.Fatalf("meta = %+v, want id=100-build cols=120 rows=40", meta)
	}
	if got := nextTFrame(t, r).chunk(t); got != "hello" {
		t.Fatalf("replay chunk = %q, want hello", got)
	}

	appendTranscript(t, filepath.Join(resultsDir(runsDir), "100-build"+agent.TranscriptExt), " world")

	f := nextTFrame(t, r)
	if got := f.chunk(t); got != " world" {
		t.Fatalf("appended chunk = %q, want ' world'", got)
	}
	if f.id != "100-build:11" {
		t.Errorf("appended chunk id = %q, want 100-build:11", f.id)
	}
}

// TestTranscriptStreamFollowsNewest covers the default follow mode: with no id it
// tails the newest transcript, so a later phase's transcript is the one streamed.
func TestTranscriptStreamFollowsNewest(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	old := writeTranscript(t, runsDir, "100-build", 80, 24, "build output")
	newer := writeTranscript(t, runsDir, "200-verify", 100, 30, "verify output")
	setMtime(t, old, time.Now().Add(-time.Minute))
	setMtime(t, newer, time.Now())

	ts := instancesServer(t, home)
	r := openTranscriptStream(t, ts, "acme", "", nil)

	if meta := nextTFrame(t, r).meta(t); meta.ID != "200-verify" {
		t.Fatalf("followed id = %q, want newest 200-verify", meta.ID)
	}
	if got := nextTFrame(t, r).chunk(t); got != "verify output" {
		t.Fatalf("followed chunk = %q, want 'verify output'", got)
	}
}

// TestTranscriptStreamPinnedReplaysFinished covers replay: a pinned id streams
// that finished phase's transcript in full, not the newest one.
func TestTranscriptStreamPinnedReplaysFinished(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	old := writeTranscript(t, runsDir, "100-build", 80, 24, "build output")
	newer := writeTranscript(t, runsDir, "200-verify", 100, 30, "verify output")
	setMtime(t, old, time.Now().Add(-time.Minute))
	setMtime(t, newer, time.Now())

	ts := instancesServer(t, home)
	r := openTranscriptStream(t, ts, "acme", "id=100-build", nil)

	if meta := nextTFrame(t, r).meta(t); meta.ID != "100-build" || meta.Cols != 80 {
		t.Fatalf("pinned meta = %+v, want 100-build cols=80", meta)
	}
	if got := nextTFrame(t, r).chunk(t); got != "build output" {
		t.Fatalf("pinned chunk = %q, want 'build output'", got)
	}
}

// TestTranscriptStreamResetsOnTruncation covers the phase-reuse case: when a
// transcript is truncated in place and rewritten, the stream emits a reset so the
// client clears its emulator, then streams the fresh content.
func TestTranscriptStreamResetsOnTruncation(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	path := writeTranscript(t, runsDir, "100-build", 80, 24, "long original output")

	ts := instancesServer(t, home)
	r := openTranscriptStream(t, ts, "acme", "", nil)

	nextTFrame(t, r).meta(t)
	if got := nextTFrame(t, r).chunk(t); got != "long original output" {
		t.Fatalf("initial chunk = %q", got)
	}

	if err := os.WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("truncate transcript: %v", err)
	}

	if f := nextTFrame(t, r); f.event != "reset" {
		t.Fatalf("frame event = %q, want reset after truncation", f.event)
	}
	if got := nextTFrame(t, r).chunk(t); got != "new" {
		t.Fatalf("post-truncation chunk = %q, want new", got)
	}
}

// TestTranscriptStreamResumesFromCursor covers a browser reconnect: a client that
// resumes from a byte offset in a transcript receives only the bytes past it, with
// no replay of what it already rendered.
func TestTranscriptStreamResumesFromCursor(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	writeTranscript(t, runsDir, "100-build", 80, 24, "hello world")

	ts := instancesServer(t, home)
	r := openTranscriptStream(t, ts, "acme", "", http.Header{"Last-Event-ID": {"100-build:5"}})

	nextTFrame(t, r).meta(t)
	if got := nextTFrame(t, r).chunk(t); got != " world" {
		t.Fatalf("resumed chunk = %q, want ' world' (no replay of 'hello')", got)
	}
}

// TestTranscriptsListNewestFirst covers the replay picker resource: the repo's
// transcripts, newest first, with the newest flagged live and the phase label
// recovered from the filename.
func TestTranscriptsListNewestFirst(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	old := writeTranscript(t, runsDir, "100-build", 80, 24, "a")
	newer := writeTranscript(t, runsDir, "200-verify", 100, 30, "bb")
	setMtime(t, old, time.Now().Add(-time.Minute))
	setMtime(t, newer, time.Now())

	ts := instancesServer(t, home)
	out := getTranscripts(t, ts, "acme")

	if out.Repo != "acme" || len(out.Transcripts) != 2 {
		t.Fatalf("transcripts = %+v, want 2 for acme", out)
	}
	first, second := out.Transcripts[0], out.Transcripts[1]
	if first.ID != "200-verify" || !first.Live || first.Label != "verify" || first.Cols != 100 {
		t.Errorf("newest = %+v, want 200-verify live label=verify cols=100", first)
	}
	if second.ID != "100-build" || second.Live || second.Label != "build" {
		t.Errorf("second = %+v, want 100-build not-live label=build", second)
	}
}

func TestTranscriptsEmpty(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	if out := getTranscripts(t, ts, "acme"); out.Transcripts == nil || len(out.Transcripts) != 0 {
		t.Errorf("transcripts = %v, want empty non-nil slice", out.Transcripts)
	}
}

func TestTranscriptUnknownRepo404(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	for _, path := range []string{"/repos/ghost/transcripts", "/repos/ghost/transcript/stream"} {
		res, err := http.Get(ts.URL + APIPrefix + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, res.StatusCode)
		}
	}
}

func TestTranscriptStreamRejectsTraversalID(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/acme/transcript/stream?id=../../etc/passwd")
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("traversal id status = %d, want 400", res.StatusCode)
	}
}

func getTranscripts(t *testing.T, ts *httptest.Server, repo string) TranscriptsResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/transcripts")
	if err != nil {
		t.Fatalf("GET transcripts: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out TranscriptsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode transcripts: %v", err)
	}
	return out
}
