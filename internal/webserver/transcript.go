package webserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

const (
	defaultTranscriptCols = 80
	defaultTranscriptRows = 24
)

// TranscriptView is one transcript session as the replay picker sees it: the id
// the stream pins to, the phase label parsed from the stem, the terminal
// dimensions, and the size/modified for ordering. Live marks the newest session —
// the one follow mode tails.
type TranscriptView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Live     bool   `json:"live"`
}

// TranscriptsResponse is the /api/v1/repos/{repo}/transcripts resource: a repo's
// transcript sessions, newest first, served from the chunk store.
type TranscriptsResponse struct {
	Repo        string           `json:"repo"`
	Transcripts []TranscriptView `json:"transcripts"`
}

// transcriptMeta is the leading SSE frame of a stream: the id of the session being
// followed and its terminal dimensions, so the client sizes its terminal before
// the first byte. A new meta with a different id means the follow target advanced.
type transcriptMeta struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// transcriptCursor is where a reconnecting SSE client resumes: the session it last
// saw and the seq within it. It only applies when the resolved target still matches
// stem — otherwise the follow target moved on and the stream replays from the top.
type transcriptCursor struct {
	stem string
	seq  int64
}

// transcriptChunkInput is one chunk in an append batch — it mirrors
// hubclient.TranscriptChunk.
type transcriptChunkInput struct {
	Stem string `json:"stem"`
	Seq  int64  `json:"seq"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Data string `json:"data"`
}

type transcriptChunkOutput struct {
	Seq  int64  `json:"seq"`
	Data string `json:"data"`
}

// transcriptChunksResponse is the /transcript/chunks poll resource the Go live
// pollers (the TUI view and `trau watch`) consume — it mirrors the shape
// hubclient decodes.
type transcriptChunksResponse struct {
	ID     string                  `json:"id"`
	Cols   int                     `json:"cols"`
	Rows   int                     `json:"rows"`
	Chunks []transcriptChunkOutput `json:"chunks"`
}

// handleTranscripts serves the repo's transcript sessions (GET) and receives the
// loop child's chunk batch (POST). Children POST chunks here instead of writing a
// .pty.log file (ADR 0008 §4); the hub appends them to the chunk store and fans
// them out to live streams.
func (s *Server) handleTranscripts(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, TranscriptsResponse{Repo: repo.Name, Transcripts: s.listTranscripts(repo.Root)})
	case http.MethodPost:
		s.appendTranscript(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listTranscripts(root string) []TranscriptView {
	sessions, err := s.transcripts.Sessions(root)
	if err != nil {
		return []TranscriptView{}
	}
	views := make([]TranscriptView, 0, len(sessions))
	for _, sess := range sessions {
		cols, rows := transcriptDims(sess.Cols, sess.Rows)
		views = append(views, TranscriptView{
			ID:       sess.Stem,
			Label:    transcriptLabel(sess.Stem),
			Cols:     cols,
			Rows:     rows,
			Size:     sess.Size,
			Modified: sess.Modified.UTC().Format(time.RFC3339),
		})
	}
	if len(views) > 0 {
		views[0].Live = true
	}
	return views
}

// appendTranscript persists a child's chunk batch to the store in order and fans
// each chunk out to live subscribers. It comes from the live loop, so it is never
// refused while a loop is running.
func (s *Server) appendTranscript(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req struct {
		Chunks []transcriptChunkInput `json:"chunks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	news := make([]hubstore.NewTranscriptChunk, 0, len(req.Chunks))
	for _, c := range req.Chunks {
		data, err := base64.StdEncoding.DecodeString(c.Data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid chunk data"})
			return
		}
		news = append(news, hubstore.NewTranscriptChunk{Stem: c.Stem, Seq: c.Seq, Cols: c.Cols, Rows: c.Rows, Data: data})
	}
	if err := s.transcripts.Append(repo.Root, news); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, c := range news {
		s.transcriptEvents.publish(liveTranscriptChunk{Root: repo.Root, Stem: c.Stem, Seq: c.Seq, Cols: c.Cols, Rows: c.Rows, Data: c.Data})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(news)})
}

// handleTranscriptChunks polls a repo's transcript chunks for the Go live pollers.
// A pinned id replays one session; follow advances to the newest at or after since.
func (s *Server) handleTranscriptChunks(w http.ResponseWriter, r *http.Request) {
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
	id := r.URL.Query().Get("id")
	if id != "" && !validTranscriptID(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid transcript id"})
		return
	}
	after := parseAfter(r.URL.Query().Get("after"))
	stem := id
	cols, rows := 0, 0
	if r.URL.Query().Get("follow") == "1" {
		newest, nc, nr, ok, err := s.transcripts.NewestStem(repo.Root, parseSinceNanos(r.URL.Query().Get("since")))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if ok && newest != id {
			stem, cols, rows, after = newest, nc, nr, -1
		}
	}
	if stem == "" {
		writeJSON(w, http.StatusOK, transcriptChunksResponse{Chunks: []transcriptChunkOutput{}})
		return
	}
	if cols == 0 && rows == 0 {
		c, rw, _, err := s.transcripts.Dims(repo.Root, stem)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		cols, rows = c, rw
	}
	chunks, err := s.transcripts.Chunks(repo.Root, stem, after)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c, rw := transcriptDims(cols, rows)
	out := transcriptChunksResponse{ID: stem, Cols: c, Rows: rw, Chunks: make([]transcriptChunkOutput, len(chunks))}
	for i, ch := range chunks {
		out.Chunks[i] = transcriptChunkOutput{Seq: ch.Seq, Data: base64.StdEncoding.EncodeToString(ch.Data)}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTranscriptStream(w http.ResponseWriter, r *http.Request) {
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
	id := r.URL.Query().Get("id")
	if id != "" && !validTranscriptID(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid transcript id"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	setSSEHeaders(w)

	// The since bound applies only to follow mode: a pinned id is an explicit
	// request for that one session, whenever it ran.
	var sinceNanos int64
	if id == "" {
		sinceNanos = parseSinceNanos(r.URL.Query().Get("since"))
	}
	sub, ch := s.transcriptEvents.subscribe()
	defer s.transcriptEvents.unsubscribe(sub)
	s.streamTranscript(r.Context(), w, flusher, ch, repo.Root, id, sinceNanos, resumeTranscriptCursor(r))
}

// streamTranscript backfills the resolved session from the chunk store, then
// forwards live chunks from the broadcaster until the client disconnects. With a
// pinned id it follows that one session; empty id follows the newest at or after
// sinceNanos, switching when a new phase starts. A silent stream sends a keepalive.
func (s *Server) streamTranscript(ctx context.Context, w io.Writer, flusher http.Flusher, ch <-chan liveTranscriptChunk, root, pinned string, sinceNanos int64, resume transcriptCursor) {
	flusher.Flush()

	curStem := ""
	var lastSeq int64 = -1
	start := func(stem string, cols, rows int) bool {
		c, rw := transcriptDims(cols, rows)
		if writeTranscriptMeta(w, stem, c, rw) != nil {
			return false
		}
		curStem = stem
		lastSeq = -1
		if resume.stem == stem {
			lastSeq = resume.seq
		}
		return true
	}

	if stem, cols, rows, ok := s.backfillTarget(root, pinned, sinceNanos); ok {
		if !start(stem, cols, rows) {
			return
		}
		chunks, err := s.transcripts.Chunks(root, stem, lastSeq)
		if err == nil {
			for _, c := range chunks {
				if writeTranscriptChunk(w, stem, c.Seq, c.Data) != nil {
					return
				}
				lastSeq = c.Seq
			}
		}
		flusher.Flush()
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case lc := <-ch:
			if lc.Root != root {
				continue
			}
			if !s.followChunk(lc, pinned, sinceNanos, &curStem, &lastSeq, start) {
				continue
			}
			if writeTranscriptChunk(w, lc.Stem, lc.Seq, lc.Data) != nil {
				return
			}
			lastSeq = lc.Seq
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// followChunk decides whether a live chunk belongs to the followed session,
// switching the follow target (and emitting a fresh meta) when a newer phase
// starts. It reports whether the chunk should be written.
func (s *Server) followChunk(lc liveTranscriptChunk, pinned string, sinceNanos int64, curStem *string, lastSeq *int64, start func(string, int, int) bool) bool {
	if pinned != "" {
		if lc.Stem != pinned {
			return false
		}
		if *curStem != pinned && !start(pinned, lc.Cols, lc.Rows) {
			return false
		}
	} else if lc.Stem != *curStem {
		if sinceNanos > 0 {
			if n, ok := transcriptStartNanos(lc.Stem); !ok || n < sinceNanos {
				return false
			}
		}
		if *curStem != "" {
			cur, _ := transcriptStartNanos(*curStem)
			if n, _ := transcriptStartNanos(lc.Stem); n < cur {
				return false
			}
		}
		if !start(lc.Stem, lc.Cols, lc.Rows) {
			return false
		}
	}
	return lc.Seq > *lastSeq
}

// backfillTarget resolves the session to replay before going live: the pinned
// session when the store holds it, else the newest at or after sinceNanos. A
// pinned session the store does not yet hold streams live-only.
func (s *Server) backfillTarget(root, pinned string, sinceNanos int64) (stem string, cols, rows int, ok bool) {
	if pinned != "" {
		c, r, found, err := s.transcripts.Dims(root, pinned)
		if err != nil || !found {
			return "", 0, 0, false
		}
		return pinned, c, r, true
	}
	stem, c, r, found, err := s.transcripts.NewestStem(root, sinceNanos)
	if err != nil || !found {
		return "", 0, 0, false
	}
	return stem, c, r, true
}

// transcriptDims applies the terminal-size defaults when a session recorded none.
func transcriptDims(cols, rows int) (int, int) {
	if cols <= 0 || rows <= 0 {
		return defaultTranscriptCols, defaultTranscriptRows
	}
	return cols, rows
}

// transcriptLabel recovers the phase label from a stem, which the agent names
// <unix-nano>-<label>.
func transcriptLabel(stem string) string {
	if i := strings.IndexByte(stem, '-'); i >= 0 {
		return stem[i+1:]
	}
	return stem
}

// transcriptStartNanos reads the session-start time encoded in a stem. It reports
// false when the stem has no leading nanosecond timestamp.
func transcriptStartNanos(stem string) (int64, bool) {
	digits := stem
	if i := strings.IndexByte(stem, '-'); i >= 0 {
		digits = stem[:i]
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseSinceNanos reads the follow-mode since bound: an RFC3339 timestamp (as the
// run page passes an instance's started_at) or a bare unix-nano value. Anything
// unparseable is no bound.
func parseSinceNanos(raw string) int64 {
	if raw == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UnixNano()
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
		return n
	}
	return 0
}

// parseAfter reads the poll cursor: the last seq a client saw, or -1 for a fresh
// read from the top (seqs start at 0).
func parseAfter(raw string) int64 {
	if raw == "" {
		return -1
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// resumeTranscriptCursor reads the resume point from a browser reconnect's
// Last-Event-ID, whose frame id is <stem>:<seq>.
func resumeTranscriptCursor(r *http.Request) transcriptCursor {
	id := r.Header.Get("Last-Event-ID")
	if id == "" {
		return transcriptCursor{seq: -1}
	}
	i := strings.LastIndexByte(id, ':')
	if i < 0 {
		return transcriptCursor{seq: -1}
	}
	seq, err := strconv.ParseInt(id[i+1:], 10, 64)
	if err != nil || seq < 0 {
		return transcriptCursor{seq: -1}
	}
	return transcriptCursor{stem: id[:i], seq: seq}
}

// validTranscriptID rejects an id that could escape a path — legitimate stems are
// a single component of <unix-nano>-<sanitized-label>.
func validTranscriptID(id string) bool {
	return !strings.ContainsAny(id, `/\`) && id != "." && id != ".."
}

func writeTranscriptMeta(w io.Writer, stem string, cols, rows int) error {
	data, err := json.Marshal(transcriptMeta{ID: stem, Cols: cols, Rows: rows})
	if err != nil {
		return nil
	}
	_, err = fmt.Fprintf(w, "event: meta\ndata: %s\n\n", data)
	return err
}

func writeTranscriptChunk(w io.Writer, stem string, seq int64, data []byte) error {
	_, err := fmt.Fprintf(w, "id: %s:%d\ndata: %s\n\n", stem, seq, base64.StdEncoding.EncodeToString(data))
	return err
}
