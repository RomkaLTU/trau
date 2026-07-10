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
// recent events in chronological order, for the feed's initial render.
type EventsResponse struct {
	Repo   string      `json:"repo"`
	Events []FeedEvent `json:"events"`
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
	events, _ := readFeed(eventsPath(repo.RunsDir))
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	if events == nil {
		events = []FeedEvent{}
	}
	writeJSON(w, http.StatusOK, EventsResponse{Repo: repo.Name, Events: events})
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
	pump(r.Context(), w, flusher, path, resumeOffset(r, path))
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

// resumeOffset picks where the stream starts reading: the Last-Event-ID header on
// a browser reconnect, then an explicit ?since cursor handed over from the recent
// resource, else the start of the last defaultBackfill events so a fresh
// connection paints a populated feed.
func resumeOffset(r *http.Request, path string) int64 {
	if off, ok := parseOffset(r.Header.Get("Last-Event-ID")); ok {
		return off
	}
	if off, ok := parseOffset(r.URL.Query().Get("since")); ok {
		return off
	}
	return backfillStart(path, defaultBackfill)
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
				off = backfillStart(path, defaultBackfill)
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
