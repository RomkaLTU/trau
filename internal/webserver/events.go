package webserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

const (
	defaultRecentEvents = 100
	maxRecentEvents     = 1000
	defaultBackfill     = 100
	streamHeartbeatIdle = 30
)

// streamPollInterval is how often the tail loop re-reads the file for appended
// events. A package var so streaming tests can tighten it.
var streamPollInterval = 500 * time.Millisecond

// FeedEvent is one durable event tagged with its byte offset in events.jsonl. The
// offset doubles as the resume cursor — it is the SSE frame id and the ?since
// value — so a reconnecting client resumes exactly after the last event it saw,
// losing and duplicating none.
type FeedEvent struct {
	ID string `json:"id"`
	event.Event
}

// EventsResponse is the /api/v1/repos/{repo}/events resource: the repo's most
// recent events in chronological order, for the feed's initial render. Cursor,
// when set, is the id an older page is fetched with via ?cursor; it is absent on
// the last page.
type EventsResponse struct {
	Repo   string      `json:"repo"`
	Events []FeedEvent `json:"events"`
	Cursor string      `json:"cursor,omitempty"`
}

// repoEvent tags a FeedEvent with its repo for the machine-wide /events/stream
// multiplex, where one connection carries every repo's events so the browser's
// per-origin connection cap does not starve a monitor watching many repos.
type repoEvent struct {
	Repo string `json:"repo"`
	FeedEvent
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	limit := clampLimit(r.URL.Query().Get("limit"))
	before, _ := parseOffset(r.URL.Query().Get("cursor"))
	events, cursor := s.recentEvents(repo, limit, before)
	writeJSON(w, http.StatusOK, EventsResponse{Repo: repo.Name, Events: events, Cursor: cursor})
}

// recentEvents serves a page of repo's events from the derived projection: up to
// limit events ending at before, newest bounded first but returned in
// chronological order, and the cursor for the next older page (empty on the last
// one). Until the projection has ingested the repo the latest page falls back to
// the durable file so the first render is never empty; older pages, which the
// file cannot key, stay empty.
func (s *Server) recentEvents(repo registry.Repo, limit int, before int64) ([]FeedEvent, string) {
	rows, err := s.stores.Derived().RecentEvents(repo.Root, limit, before)
	if err != nil {
		logger.Verbosef("events query %s: %v", repo.Name, err)
		rows = nil
	}
	if len(rows) == 0 && before == 0 {
		return fileFeed(eventsPath(repo.RunsDir), limit), ""
	}
	events := feedFromRows(rows)
	cursor := ""
	if len(events) == limit {
		cursor = events[0].ID
	}
	return events, cursor
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	path := eventsPath(repo.RunsDir)
	off, resumed := explicitResume(r)
	if !resumed {
		backfill, start := s.streamBackfill(repo)
		for _, ev := range backfill {
			if emitEvent(w, "", ev) != nil {
				return
			}
		}
		if len(backfill) > 0 {
			flusher.Flush()
		}
		off = start
	}
	pump(r.Context(), w, flusher, path, off)
}

func (s *Server) handleAllEventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	s.pumpAll(r.Context(), w, flusher)
}

// explicitResume reports where a reconnecting client resumes from — the
// Last-Event-ID header a browser replays, then an explicit ?since cursor handed
// over from the recent resource — and whether one was given. Both are byte
// offsets into the file the live tail reads directly, so a reconnect never
// re-parses the log. Without one the caller backfills from the projection.
func explicitResume(r *http.Request) (int64, bool) {
	if off, ok := parseOffset(r.Header.Get("Last-Event-ID")); ok {
		return off, true
	}
	if off, ok := parseOffset(r.URL.Query().Get("since")); ok {
		return off, true
	}
	return 0, false
}

// streamBackfill serves a fresh connection's initial events from the derived
// projection — repo's most recent events — and returns the offset the live file
// tail resumes from: the byte offset just past the last event served, so tailing
// picks up only what the projection has yet to ingest, with neither gap nor
// repeat. Until the projection has ingested the repo it returns no events and the
// file's own tail-start, so the connection still paints a populated feed.
func (s *Server) streamBackfill(repo registry.Repo) ([]FeedEvent, int64) {
	rows, err := s.stores.Derived().RecentEvents(repo.Root, defaultBackfill, 0)
	if err != nil {
		logger.Verbosef("events backfill %s: %v", repo.Name, err)
		rows = nil
	}
	if len(rows) == 0 {
		return nil, backfillStart(eventsPath(repo.RunsDir), defaultBackfill)
	}
	return feedFromRows(rows), rows[0].Seq
}

// feedFromRows reverses the newest-first rows into the feed's chronological order,
// reconstructing each FeedEvent so its JSON is identical to the file-served shape.
func feedFromRows(rows []hubstore.EventRow) []FeedEvent {
	out := make([]FeedEvent, len(rows))
	for i, row := range rows {
		out[len(rows)-1-i] = feedEventFromRow(row)
	}
	return out
}

func feedEventFromRow(row hubstore.EventRow) FeedEvent {
	return FeedEvent{
		ID: strconv.FormatInt(row.Seq, 10),
		Event: event.Event{
			Time:   row.TS,
			Kind:   row.Kind,
			Phase:  row.Phase,
			Msg:    row.Msg,
			Fields: unmarshalFields(row.Fields),
		},
	}
}

// unmarshalFields restores the event fields the ingester stored as a JSON string,
// leaving an empty column as a nil map so omitempty drops it exactly as the
// file-served event does.
func unmarshalFields(s string) map[string]any {
	if s == "" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

// fileFeed is the cold-start fallback: until the projection has ingested a repo,
// the last limit events are read straight from the durable file so the first
// render is never empty.
func fileFeed(path string, limit int) []FeedEvent {
	events, _ := readFeed(path)
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	if events == nil {
		events = []FeedEvent{}
	}
	return events
}

// pump tails path from offset, emitting each new event as an SSE frame until the
// client disconnects. Between appends it sends a keepalive comment so the
// connection — and its liveness — is not left silent.
func pump(ctx context.Context, w io.Writer, flusher http.Flusher, path string, offset int64) {
	flusher.Flush()
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	idle := 0
	for {
		next, wrote, err := emitFrom(w, flusher, path, "", offset)
		if err != nil {
			return
		}
		offset = next
		switch {
		case wrote:
			idle = 0
		case idle >= streamHeartbeatIdle:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			idle = 0
		default:
			idle++
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pumpAll tails every known repo onto a single SSE stream, tagging each frame
// with its repo. New repos that begin streaming after the connection opens are
// picked up on the next poll and backfilled from their recent tail. One
// connection serves the whole machine-wide monitor, so the number of repos never
// runs into the browser's per-origin connection cap.
func (s *Server) pumpAll(ctx context.Context, w io.Writer, flusher http.Flusher) {
	flusher.Flush()
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()
	offsets := map[string]int64{}
	idle := 0
	for {
		wrote := false
		for _, repo := range s.streamRepos() {
			path := eventsPath(repo.RunsDir)
			off, seen := offsets[repo.Name]
			if !seen {
				backfill, start := s.streamBackfill(repo)
				for _, ev := range backfill {
					if emitEvent(w, repo.Name, ev) != nil {
						return
					}
				}
				if len(backfill) > 0 {
					flusher.Flush()
					wrote = true
				}
				off = start
			}
			next, n, err := emitFrom(w, flusher, path, repo.Name, off)
			if err != nil {
				return
			}
			offsets[repo.Name] = next
			wrote = wrote || n
		}
		switch {
		case wrote:
			idle = 0
		case idle >= streamHeartbeatIdle:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			idle = 0
		default:
			idle++
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// streamRepos resolves the repos the multiplex tails, unioning live loops' repos
// so a just-started loop joins the stream — mirroring findRepo.
func (s *Server) streamRepos() []registry.Repo {
	return s.knownRepos(registry.Live(s.home))
}

// emitFrom reads every complete event appended since offset, writes one SSE frame
// each, and returns the new offset. A missing file is not an error — the loop the
// hub did not start may not have written events yet — so the connection stays open.
// A non-empty repo tags each frame for the machine-wide multiplex.
func emitFrom(w io.Writer, flusher http.Flusher, path, repo string, offset int64) (int64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, false, nil
	}
	defer func() { _ = f.Close() }()

	if info, err := f.Stat(); err == nil && offset > info.Size() {
		offset = info.Size()
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, false, nil
	}

	events, next := scanFeed(bufio.NewReader(f), offset)
	for _, ev := range events {
		if err := emitEvent(w, repo, ev); err != nil {
			return next, false, err
		}
	}
	if len(events) > 0 {
		flusher.Flush()
	}
	return next, len(events) > 0, nil
}

// scanFeed parses the complete newline-terminated lines from r, tagging each with
// the byte offset at its end — the cursor a client resumes from. A trailing line
// without a newline is a half-written record and is left for the next read.
func scanFeed(r *bufio.Reader, base int64) ([]FeedEvent, int64) {
	off := base
	var out []FeedEvent
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		off += int64(len(line))
		var ev event.Event
		if json.Unmarshal(line[:len(line)-1], &ev) != nil {
			continue
		}
		out = append(out, FeedEvent{ID: strconv.FormatInt(off, 10), Event: ev})
	}
	return out, off
}

// backfillStart returns the offset at which the last n events begin, or 0 when the
// file holds n or fewer.
func backfillStart(path string, n int) int64 {
	events, _ := readFeed(path)
	if len(events) <= n {
		return 0
	}
	off, _ := parseOffset(events[len(events)-n-1].ID)
	return off
}

func readFeed(path string) ([]FeedEvent, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = f.Close() }()
	return scanFeed(bufio.NewReader(f), 0)
}

// emitEvent writes one SSE frame for ev. An untagged frame carries the bare
// FeedEvent for the per-repo stream; a repo-tagged frame carries the repo and a
// repo-qualified id for the machine-wide multiplex.
func emitEvent(w io.Writer, repo string, ev FeedEvent) error {
	if repo == "" {
		return writeSSE(w, ev.ID, ev)
	}
	return writeSSE(w, repo+":"+ev.ID, repoEvent{Repo: repo, FeedEvent: ev})
}

func writeSSE(w io.Writer, id string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	_, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", id, data)
	return err
}

func eventsPath(runsDir string) string {
	return filepath.Join(runsDir, "events.jsonl")
}

func parseOffset(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	off, err := strconv.ParseInt(s, 10, 64)
	if err != nil || off < 0 {
		return 0, false
	}
	return off, true
}

func clampLimit(raw string) int {
	if raw == "" {
		return defaultRecentEvents
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultRecentEvents
	}
	if n > maxRecentEvents {
		return maxRecentEvents
	}
	return n
}
