package webserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
)

// fastPoll tightens the tail poll so the streaming tests observe an appended
// event within a few milliseconds instead of the production half-second.
func fastPoll(t *testing.T) {
	t.Helper()
	prev := streamPollInterval
	streamPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { streamPollInterval = prev })
}

// appendEvent writes one event line to the repo's events.jsonl exactly as the
// loop does — so the fixtures exercise the real on-disk stream, not a mock. It is
// the "written by a loop the hub did not start" case: the hub only ever tails.
func appendEvent(t *testing.T, runsDir string, ev event.Event) {
	t.Helper()
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	f, err := os.OpenFile(eventsPath(runsDir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close events: %v", err)
	}
}

type sseFrame struct {
	id   string
	data string
}

// openStream connects to a repo's SSE endpoint and returns a reader over its
// frames.
func openStream(t *testing.T, ts *httptest.Server, repo string, header http.Header) *bufio.Reader {
	t.Helper()
	return openSSE(t, ts, APIPrefix+"/repos/"+repo+"/events/stream", header)
}

// openAllStream connects to the machine-wide multiplexed SSE endpoint.
func openAllStream(t *testing.T, ts *httptest.Server, header http.Header) *bufio.Reader {
	t.Helper()
	return openSSE(t, ts, APIPrefix+"/events/stream", header)
}

// openSSE opens an SSE stream at path and returns a reader over its frames. A
// watchdog cancels the request after a few seconds so a stuck read fails the test
// instead of hanging.
func openSSE(t *testing.T, ts *httptest.Server, path string, header http.Header) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("stream request: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("stream status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		cancel()
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	timer := time.AfterFunc(5*time.Second, cancel)
	t.Cleanup(func() {
		timer.Stop()
		cancel()
		_ = res.Body.Close()
	})
	return bufio.NewReader(res.Body)
}

// nextData reads frames until a data frame arrives, skipping keepalive comments.
func nextData(t *testing.T, r *bufio.Reader) sseFrame {
	t.Helper()
	for {
		var f sseFrame
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
			case strings.HasPrefix(line, "id:"):
				f.id = strings.TrimSpace(line[len("id:"):])
			case strings.HasPrefix(line, "data:"):
				f.data = strings.TrimSpace(line[len("data:"):])
			}
		}
		if f.data != "" {
			return f
		}
	}
}

func decodeFrame(t *testing.T, f sseFrame) FeedEvent {
	t.Helper()
	var fe FeedEvent
	if err := json.Unmarshal([]byte(f.data), &fe); err != nil {
		t.Fatalf("decode frame data %q: %v", f.data, err)
	}
	if fe.ID != f.id {
		t.Errorf("frame id %q != data id %q", f.id, fe.ID)
	}
	return fe
}

func offset(t *testing.T, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("parse id %q: %v", id, err)
	}
	return n
}

// TestEventStreamBackfillsThenStreamsAppends is the streaming contract test: a
// fresh connection first replays the recent events, then an event appended to the
// file mid-request surfaces as the next SSE frame — with a strictly larger id and
// no page refresh.
func TestEventStreamBackfillsThenStreamsAppends(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	appendEvent(t, runsDir, event.Event{Time: "t1", Kind: "agent_call", Phase: "build"})
	appendEvent(t, runsDir, event.Event{Time: "t2", Kind: "usage_window"})

	ts := instancesServer(t, home)
	r := openStream(t, ts, "acme", nil)

	f1 := decodeFrame(t, nextData(t, r))
	f2 := decodeFrame(t, nextData(t, r))
	if f1.Kind != "agent_call" || f2.Kind != "usage_window" {
		t.Fatalf("backfill kinds = %q, %q, want agent_call, usage_window", f1.Kind, f2.Kind)
	}
	if offset(t, f2.ID) <= offset(t, f1.ID) {
		t.Errorf("ids not increasing: %s then %s", f1.ID, f2.ID)
	}

	appendEvent(t, runsDir, event.Event{Time: "t3", Kind: "cost_anomaly", Msg: "spike"})

	f3 := decodeFrame(t, nextData(t, r))
	if f3.Kind != "cost_anomaly" || f3.Msg != "spike" {
		t.Fatalf("streamed frame = %q/%q, want cost_anomaly/spike", f3.Kind, f3.Msg)
	}
	if offset(t, f3.ID) <= offset(t, f2.ID) {
		t.Errorf("appended id %s not past backfill id %s", f3.ID, f2.ID)
	}
}

// TestEventStreamResumeWithoutDupes covers the reconnect path: a client that
// resumes from the id of the second event receives only the third — the earlier
// events are neither lost nor replayed.
func TestEventStreamResumeWithoutDupes(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	appendEvent(t, runsDir, event.Event{Time: "t1", Kind: "agent_call", Msg: "a"})
	appendEvent(t, runsDir, event.Event{Time: "t2", Kind: "agent_call", Msg: "b"})
	appendEvent(t, runsDir, event.Event{Time: "t3", Kind: "cost_anomaly", Msg: "c"})

	ts := instancesServer(t, home)
	recent := getEvents(t, ts, "acme", "")
	if len(recent.Events) != 3 {
		t.Fatalf("recent = %d events, want 3", len(recent.Events))
	}
	resumeFrom := recent.Events[1].ID

	r := openStream(t, ts, "acme", http.Header{"Last-Event-ID": {resumeFrom}})
	f := decodeFrame(t, nextData(t, r))
	if f.Msg != "c" {
		t.Fatalf("resumed frame msg = %q, want c (no replay of a/b)", f.Msg)
	}
	if f.ID != recent.Events[2].ID {
		t.Errorf("resumed id = %q, want %q", f.ID, recent.Events[2].ID)
	}
}

func getEvents(t *testing.T, ts *httptest.Server, repo, query string) EventsResponse {
	t.Helper()
	url := ts.URL + APIPrefix + "/repos/" + repo + "/events"
	if query != "" {
		url += "?" + query
	}
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var out EventsResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return out
}

// TestRecentEventsResourceReturnsLastN covers the initial-render resource: it
// returns the last N events in chronological order, each tagged with the resume
// cursor the stream continues from.
func TestRecentEventsResourceReturnsLastN(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	for i := 1; i <= 5; i++ {
		appendEvent(t, runsDir, event.Event{Time: fmt.Sprintf("t%d", i), Kind: "agent_call", Msg: strconv.Itoa(i)})
	}

	ts := instancesServer(t, home)
	out := getEvents(t, ts, "acme", "limit=3")

	if out.Repo != "acme" {
		t.Errorf("repo = %q, want acme", out.Repo)
	}
	if len(out.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(out.Events))
	}
	wantMsgs := []string{"3", "4", "5"}
	prev := int64(-1)
	for i, ev := range out.Events {
		if ev.Msg != wantMsgs[i] {
			t.Errorf("event %d msg = %q, want %q", i, ev.Msg, wantMsgs[i])
		}
		if off := offset(t, ev.ID); off <= prev {
			t.Errorf("event %d id %s not increasing (prev %d)", i, ev.ID, prev)
		} else {
			prev = off
		}
	}
}

// TestRecentEventsEmpty covers a known repo whose loop has not written any events
// yet: an empty list, never a null.
func TestRecentEventsEmpty(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	if out := getEvents(t, ts, "acme", ""); out.Events == nil || len(out.Events) != 0 {
		t.Errorf("events = %v, want empty non-nil slice", out.Events)
	}
}

func TestEventsUnknownRepo404(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	for _, path := range []string{"/repos/ghost/events", "/repos/ghost/events/stream"} {
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

// repoFrame is a decoded frame from the machine-wide multiplex: its repo tag plus
// the event.
type repoFrame struct {
	Repo string `json:"repo"`
	FeedEvent
}

func decodeRepoFrame(t *testing.T, f sseFrame) repoFrame {
	t.Helper()
	var rf repoFrame
	if err := json.Unmarshal([]byte(f.data), &rf); err != nil {
		t.Fatalf("decode multiplex frame %q: %v", f.data, err)
	}
	return rf
}

// TestAllEventStreamMultiplexesRepos is the machine-wide contract test: one
// connection backfills every known repo's tail, tags each frame with its repo,
// and streams an append to any repo — the fix for the per-origin connection cap
// that stranded feeds past the sixth on the Instances page.
func TestAllEventStreamMultiplexesRepos(t *testing.T) {
	fastPoll(t)
	home := t.TempDir()
	dirs := seedRepos(t, home, "alpha", "bravo")
	appendEvent(t, dirs["alpha"], event.Event{Time: "t1", Kind: "agent_call", Msg: "a"})
	appendEvent(t, dirs["bravo"], event.Event{Time: "t1", Kind: "usage_window", Msg: "b"})

	ts := instancesServer(t, home)
	r := openAllStream(t, ts, nil)

	backfill := map[string]string{}
	for i := 0; i < 2; i++ {
		rf := decodeRepoFrame(t, nextData(t, r))
		backfill[rf.Repo] = rf.Msg
	}
	if backfill["alpha"] != "a" || backfill["bravo"] != "b" {
		t.Fatalf("multiplexed backfill = %v, want alpha:a bravo:b", backfill)
	}

	appendEvent(t, dirs["bravo"], event.Event{Time: "t2", Kind: "cost_anomaly", Msg: "spike"})
	rf := decodeRepoFrame(t, nextData(t, r))
	if rf.Repo != "bravo" || rf.Msg != "spike" {
		t.Fatalf("streamed frame = %s/%s, want bravo/spike", rf.Repo, rf.Msg)
	}
}

func TestAllEventStreamRejectsNonGET(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Post(ts.URL+APIPrefix+"/events/stream", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST stream: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}

func TestRecentEventsRejectsNonGET(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	res, err := http.Post(ts.URL+APIPrefix+"/repos/acme/events", "application/json", nil)
	if err != nil {
		t.Fatalf("POST events: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", res.StatusCode)
	}
}
