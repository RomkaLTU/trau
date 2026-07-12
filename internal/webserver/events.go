package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
)

// streamHeartbeat is how long a live stream stays silent before sending a keepalive
// comment. A package var so streaming tests can tighten it.
var streamHeartbeat = 30 * time.Second

// FeedEvent is one persisted event tagged with the monotonic id that orders the
// feed. The id doubles as the resume cursor — it is the SSE frame id and the
// ?since / ?cursor value — so a reconnecting client resumes exactly after the last
// event it saw, losing and duplicating none (ADR 0008).
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

// handleEvents serves the repo's recent events (GET) and receives the loop child's
// event batch (POST). Children POST events here instead of appending a log file
// (ADR 0008); the hub appends them to the authoritative table and fans them out to
// live streams.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit := clampLimit(r.URL.Query().Get("limit"))
		before, _ := parseCursor(r.URL.Query().Get("cursor"))
		events, cursor := s.recentEvents(repo, limit, before)
		writeJSON(w, http.StatusOK, EventsResponse{Repo: repo.Name, Events: events, Cursor: cursor})
	case http.MethodPost:
		s.appendEvents(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// appendEventInput is one event in an append batch, mirroring hubclient.Event.
type appendEventInput struct {
	TS     string `json:"ts"`
	Kind   string `json:"kind"`
	Phase  string `json:"phase"`
	Msg    string `json:"msg"`
	Fields string `json:"fields"`
}

// appendEvents persists a child's event batch to the authoritative table in order
// and fans each row out to live subscribers. The ids the table assigns preserve
// the batch's order, so a run's events never reorder under batching.
func (s *Server) appendEvents(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req struct {
		Events []appendEventInput `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	news := make([]hubstore.NewEvent, len(req.Events))
	for i, e := range req.Events {
		news[i] = hubstore.NewEvent{TS: e.TS, Kind: e.Kind, Phase: e.Phase, Msg: e.Msg, Fields: e.Fields}
	}
	rows, err := s.stores.Events().Append(repo.Root, news)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, row := range rows {
		s.publishEvent(repo.Root, repo.Name, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(rows)})
}

// publishEvent hands a persisted row to the live fan-out, tagged with the repo the
// per-repo stream filters on and the name the machine-wide stream labels frames by.
func (s *Server) publishEvent(root, name string, row hubstore.EventRow) {
	s.events.publish(liveEvent{Root: root, Name: name, Event: feedEventFromRow(row)})
}

// recentEvents serves a page of repo's events from the authoritative table: up to
// limit events ending at before, newest bounded first but returned in
// chronological order, and the cursor for the next older page (empty on the last
// one).
func (s *Server) recentEvents(repo registry.Repo, limit int, before int64) ([]FeedEvent, string) {
	rows, err := s.stores.Events().Recent(repo.Root, limit, before)
	if err != nil {
		logger.Verbosef("events query %s: %v", repo.Name, err)
		return []FeedEvent{}, ""
	}
	events := feedFromRows(rows)
	cursor := ""
	if len(events) == limit && len(events) > 0 {
		cursor = events[0].ID
	}
	return events, cursor
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	setSSEHeaders(w)

	sub, ch := s.events.subscribe()
	defer s.events.unsubscribe(sub)

	after, resumed := eventResumeCursor(r)
	var backfill []FeedEvent
	if resumed {
		backfill = s.eventsSince(repo, after)
	} else {
		backfill = s.recentFeed(repo, defaultBackfill)
	}
	last := after
	for _, ev := range backfill {
		if emitEvent(w, "", ev) != nil {
			return
		}
		if id, ok := parseCursor(ev.ID); ok {
			last = id
		}
	}
	flusher.Flush()
	s.streamLive(r.Context(), w, flusher, ch, repo.Root, last)
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
	setSSEHeaders(w)

	sub, ch := s.events.subscribe()
	defer s.events.unsubscribe(sub)

	after, resumed := eventResumeCursor(r)
	var backfill []liveEvent
	if resumed {
		backfill = s.allEventsSince(after)
	} else {
		backfill = s.recentAllFeed(defaultBackfill)
	}
	last := after
	for _, ev := range backfill {
		if emitEvent(w, ev.Name, ev.Event) != nil {
			return
		}
		if id, ok := parseCursor(ev.Event.ID); ok {
			last = id
		}
	}
	flusher.Flush()
	s.streamLive(r.Context(), w, flusher, ch, "", last)
}

// streamLive forwards live events to a connected client until it disconnects.
// filterRoot, when set, keeps only that repo's events and emits untagged frames —
// the per-repo stream; empty filterRoot emits every repo's events tagged with its
// name — the machine-wide stream. Events already covered by the backfill (id at or
// below last) are skipped, so backfill and live join without a gap or a repeat. A
// silent stream sends a keepalive comment so its liveness is never in doubt.
func (s *Server) streamLive(ctx context.Context, w io.Writer, flusher http.Flusher, ch <-chan liveEvent, filterRoot string, last int64) {
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			if filterRoot != "" && ev.Root != filterRoot {
				continue
			}
			if id, ok := parseCursor(ev.Event.ID); ok {
				if id <= last {
					continue
				}
				last = id
			}
			tag := ""
			if filterRoot == "" {
				tag = ev.Name
			}
			if emitEvent(w, tag, ev.Event) != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// recentFeed backfills a fresh per-repo connection with the repo's most recent
// events in chronological order.
func (s *Server) recentFeed(repo registry.Repo, limit int) []FeedEvent {
	rows, err := s.stores.Events().Recent(repo.Root, limit, 0)
	if err != nil {
		logger.Verbosef("events backfill %s: %v", repo.Name, err)
		return nil
	}
	return feedFromRows(rows)
}

// eventsSince backfills a reconnecting per-repo connection with everything the repo
// recorded after the client's last-seen id.
func (s *Server) eventsSince(repo registry.Repo, after int64) []FeedEvent {
	rows, err := s.stores.Events().Since(repo.Root, after)
	if err != nil {
		logger.Verbosef("events resume %s: %v", repo.Name, err)
		return nil
	}
	out := make([]FeedEvent, len(rows))
	for i, row := range rows {
		out[i] = feedEventFromRow(row)
	}
	return out
}

// recentAllFeed backfills a fresh machine-wide connection with the most recent
// events across every repo, in chronological order.
func (s *Server) recentAllFeed(limit int) []liveEvent {
	rows, err := s.stores.Events().RecentAll(limit, 0)
	if err != nil {
		logger.Verbosef("all-events backfill: %v", err)
		return nil
	}
	reverseRepoRows(rows)
	return s.liveEventsFromRows(rows)
}

// allEventsSince backfills a reconnecting machine-wide connection with every repo's
// events after the client's last-seen id.
func (s *Server) allEventsSince(after int64) []liveEvent {
	rows, err := s.stores.Events().SinceAll(after)
	if err != nil {
		logger.Verbosef("all-events resume: %v", err)
		return nil
	}
	return s.liveEventsFromRows(rows)
}

// liveEventsFromRows resolves each store row's repo root to the display name the
// machine-wide stream tags frames by, falling back to the root's base for a repo
// the hub no longer tracks.
func (s *Server) liveEventsFromRows(rows []hubstore.RepoEventRow) []liveEvent {
	names := s.repoNames()
	out := make([]liveEvent, 0, len(rows))
	for _, r := range rows {
		name := names[r.Repo]
		if name == "" {
			name = filepath.Base(r.Repo)
		}
		out = append(out, liveEvent{Root: r.Repo, Name: name, Event: feedEventFromRow(r.EventRow)})
	}
	return out
}

func (s *Server) repoNames() map[string]string {
	repos := s.streamRepos()
	m := make(map[string]string, len(repos))
	for _, repo := range repos {
		m[repo.Root] = repo.Name
	}
	return m
}

// streamRepos resolves the repos the machine-wide feed knows, unioning live loops'
// repos so a just-started loop is named — mirroring findRepo.
func (s *Server) streamRepos() []registry.Repo {
	return s.knownRepos(s.liveInstances())
}

// feedFromRows reverses the newest-first rows into the feed's chronological order.
func feedFromRows(rows []hubstore.EventRow) []FeedEvent {
	out := make([]FeedEvent, len(rows))
	for i, row := range rows {
		out[len(rows)-1-i] = feedEventFromRow(row)
	}
	return out
}

func reverseRepoRows(rows []hubstore.RepoEventRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func feedEventFromRow(row hubstore.EventRow) FeedEvent {
	return FeedEvent{
		ID: strconv.FormatInt(row.ID, 10),
		Event: event.Event{
			Time:   row.TS,
			Kind:   row.Kind,
			Phase:  row.Phase,
			Msg:    row.Msg,
			Fields: unmarshalFields(row.Fields),
		},
	}
}

// marshalFields renders an event's fields bag to the JSON object string the store
// keeps, collapsing an empty bag to the empty column.
func marshalFields(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalFields restores the event fields the store holds as a JSON string,
// leaving an empty column as a nil map so omitempty drops it from the wire shape.
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

func setSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

// eventResumeCursor reports where a reconnecting client resumes from — the
// Last-Event-ID header a browser replays, then an explicit ?since cursor — and
// whether one was given. Both are event ids the table backfills strictly after.
// Without one the caller backfills the recent tail.
func eventResumeCursor(r *http.Request) (int64, bool) {
	if id, ok := parseCursor(r.Header.Get("Last-Event-ID")); ok {
		return id, true
	}
	if id, ok := parseCursor(r.URL.Query().Get("since")); ok {
		return id, true
	}
	return 0, false
}

// emitEvent writes one SSE frame for ev. An untagged frame carries the bare
// FeedEvent for the per-repo stream; a tagged frame carries the repo name for the
// machine-wide multiplex. Both use the event's global id as the frame id, so a
// reconnect resumes from it on either stream.
func emitEvent(w io.Writer, tag string, ev FeedEvent) error {
	if tag == "" {
		return writeSSE(w, ev.ID, ev)
	}
	return writeSSE(w, ev.ID, repoEvent{Repo: tag, FeedEvent: ev})
}

func writeSSE(w io.Writer, id string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	_, err = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", id, data)
	return err
}

func parseCursor(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
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
