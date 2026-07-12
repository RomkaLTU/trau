package webserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// postEvents sends a batch through the hub's append endpoint exactly as the loop
// child does — so the fixtures exercise the real write path, appending to the
// authoritative table and fanning out to live streams.
func postEvents(t *testing.T, ts *httptest.Server, repo string, evs ...hubclient.Event) {
	t.Helper()
	body, err := json.Marshal(struct {
		Events []hubclient.Event `json:"events"`
	}{Events: evs})
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST events: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST events status = %d, want 200", res.StatusCode)
	}
}

func ev(kind, msg string) hubclient.Event {
	return hubclient.Event{TS: "t", Kind: kind, Msg: msg}
}

type sseFrame struct {
	id   string
	data string
}

func openStream(t *testing.T, ts *httptest.Server, repo string, header http.Header) *bufio.Reader {
	t.Helper()
	return openSSE(t, ts, APIPrefix+"/repos/"+repo+"/events/stream", header)
}

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

func idOf(t *testing.T, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("parse id %q: %v", id, err)
	}
	return n
}

// TestEventStreamBackfillsThenStreamsAppends is the streaming contract test: a
// fresh connection first replays the recent events, then an event posted mid-request
// surfaces as the next SSE frame — with a strictly larger id and no page refresh.
func TestEventStreamBackfillsThenStreamsAppends(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	postEvents(t, ts, "acme", ev("agent_call", "a"), ev("usage_window", "b"))

	r := openStream(t, ts, "acme", nil)

	f1 := decodeFrame(t, nextData(t, r))
	f2 := decodeFrame(t, nextData(t, r))
	if f1.Kind != "agent_call" || f2.Kind != "usage_window" {
		t.Fatalf("backfill kinds = %q, %q, want agent_call, usage_window", f1.Kind, f2.Kind)
	}
	if idOf(t, f2.ID) <= idOf(t, f1.ID) {
		t.Errorf("ids not increasing: %s then %s", f1.ID, f2.ID)
	}

	postEvents(t, ts, "acme", ev("cost_anomaly", "spike"))

	f3 := decodeFrame(t, nextData(t, r))
	if f3.Kind != "cost_anomaly" || f3.Msg != "spike" {
		t.Fatalf("streamed frame = %q/%q, want cost_anomaly/spike", f3.Kind, f3.Msg)
	}
	if idOf(t, f3.ID) <= idOf(t, f2.ID) {
		t.Errorf("posted id %s not past backfill id %s", f3.ID, f2.ID)
	}
}

// TestEventStreamResumeWithoutDupes covers the reconnect path: a client that
// resumes from the id of the second event receives only the third — the earlier
// events are neither lost nor replayed.
func TestEventStreamResumeWithoutDupes(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	postEvents(t, ts, "acme", ev("agent_call", "a"), ev("agent_call", "b"), ev("cost_anomaly", "c"))

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
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	for i := 1; i <= 5; i++ {
		postEvents(t, ts, "acme", ev("agent_call", strconv.Itoa(i)))
	}

	out := getEvents(t, ts, "acme", "limit=3")
	if out.Repo != "acme" {
		t.Errorf("repo = %q, want acme", out.Repo)
	}
	if len(out.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(out.Events))
	}
	wantMsgs := []string{"3", "4", "5"}
	prev := int64(-1)
	for i, e := range out.Events {
		if e.Msg != wantMsgs[i] {
			t.Errorf("event %d msg = %q, want %q", i, e.Msg, wantMsgs[i])
		}
		if id := idOf(t, e.ID); id <= prev {
			t.Errorf("event %d id %s not increasing (prev %d)", i, e.ID, prev)
		} else {
			prev = id
		}
	}
}

// TestEventsPaginate walks the cursor from the newest page to the oldest with no
// overlap and stable order, and confirms an event's fields survive the round trip.
func TestEventsPaginate(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	for i := 1; i <= 5; i++ {
		e := ev("agent_call", strconv.Itoa(i))
		e.Fields = fmt.Sprintf(`{"n":%d}`, i)
		postEvents(t, ts, "acme", e)
	}

	p0 := getEvents(t, ts, "acme", "limit=2")
	if got := msgLine(p0.Events); got != "45" {
		t.Fatalf("page 0 = %q, want 45", got)
	}
	if p0.Cursor == "" {
		t.Fatal("page 0 missing cursor for older page")
	}

	p1 := getEvents(t, ts, "acme", "limit=2&cursor="+p0.Cursor)
	if got := msgLine(p1.Events); got != "23" {
		t.Fatalf("page 1 = %q, want 23", got)
	}
	if idOf(t, p1.Events[len(p1.Events)-1].ID) >= idOf(t, p0.Events[0].ID) {
		t.Errorf("page 1 overlaps page 0: %s !< %s", p1.Events[len(p1.Events)-1].ID, p0.Events[0].ID)
	}

	p2 := getEvents(t, ts, "acme", "limit=2&cursor="+p1.Cursor)
	if got := msgLine(p2.Events); got != "1" {
		t.Fatalf("page 2 = %q, want 1", got)
	}
	if p2.Cursor != "" {
		t.Errorf("last page cursor = %q, want empty", p2.Cursor)
	}
	if p2.Events[0].Fields["n"] != float64(1) {
		t.Errorf("fields not preserved: %v", p2.Events[0].Fields)
	}
}

func msgLine(events []FeedEvent) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.Msg)
	}
	return b.String()
}

// TestRecentEventsEmpty covers a known repo whose loop has posted no events yet:
// an empty list, never a null.
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

func TestAppendEventsBadBody(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	res, err := http.Post(ts.URL+APIPrefix+"/repos/acme/events", "application/json", strings.NewReader("{bad"))
	if err != nil {
		t.Fatalf("POST events: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad body status = %d, want 400", res.StatusCode)
	}
}

func TestEventsRejectsBadMethod(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	req, err := http.NewRequest(http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE events: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE status = %d, want 405", res.StatusCode)
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
// connection backfills every known repo's tail, tags each frame with its repo, and
// streams a post to any repo — the fix for the per-origin connection cap that
// stranded feeds past the sixth on the Instances page.
func TestAllEventStreamMultiplexesRepos(t *testing.T) {
	home := t.TempDir()
	seedRepos(t, home, "alpha", "bravo")
	ts := instancesServer(t, home)
	postEvents(t, ts, "alpha", ev("agent_call", "a"))
	postEvents(t, ts, "bravo", ev("usage_window", "b"))

	r := openAllStream(t, ts, nil)

	backfill := map[string]string{}
	for i := 0; i < 2; i++ {
		rf := decodeRepoFrame(t, nextData(t, r))
		backfill[rf.Repo] = rf.Msg
	}
	if backfill["alpha"] != "a" || backfill["bravo"] != "b" {
		t.Fatalf("multiplexed backfill = %v, want alpha:a bravo:b", backfill)
	}

	postEvents(t, ts, "bravo", ev("cost_anomaly", "spike"))
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
