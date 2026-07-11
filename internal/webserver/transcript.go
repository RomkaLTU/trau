package webserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
)

// TranscriptView is one phase's raw PTY transcript as the replay picker sees it:
// the id the stream pins to, the phase label parsed from the filename, the PTY
// dimensions to size the terminal, and the size/mtime for ordering. Live marks
// the newest transcript — the one the follow-mode stream tails.
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
// phase transcripts, newest first. It reads the agent-results directory directly,
// so it lists transcripts for loops the hub never started.
type TranscriptsResponse struct {
	Repo        string           `json:"repo"`
	Transcripts []TranscriptView `json:"transcripts"`
}

// transcriptMeta is the leading SSE frame of a stream: the id of the transcript
// being followed and the PTY dimensions the agent recorded, so the client sizes
// its terminal before the first byte lands. A new meta with a different id means
// the follow target advanced to a new phase and the client resets.
type transcriptMeta struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// transcriptCursor is where a reconnecting client resumes: the stem it last saw
// and the byte offset within it. It only applies when the resolved target still
// matches stem — otherwise the follow target moved on and the stream replays the
// new transcript from the top.
type transcriptCursor struct {
	stem   string
	offset int64
}

const (
	defaultTranscriptCols = 80
	defaultTranscriptRows = 24
)

func (s *Server) handleTranscripts(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, TranscriptsResponse{
		Repo:        repo.Name,
		Transcripts: listTranscripts(resultsDir(repo.RunsDir)),
	})
}

func (s *Server) handleTranscriptStream(w http.ResponseWriter, r *http.Request) {
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

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// The since bound applies only to follow mode: a pinned id is an explicit
	// request for that one transcript, whenever its session ran.
	var sinceNanos int64
	if id == "" {
		sinceNanos = parseSinceNanos(r.URL.Query().Get("since"))
	}
	pumpTranscript(r.Context(), w, flusher, resultsDir(repo.RunsDir), id, resumeCursor(r), sinceNanos)
}

// pumpTranscript streams a repo's transcript as raw PTY bytes until the client
// disconnects. With no pinned id it follows the newest transcript, re-resolving
// each tick so it advances across phase boundaries; a pinned id replays one
// finished phase. A non-zero sinceNanos bounds follow mode to sessions that
// started at or after it, so a fresh run page never time-travels into a previous
// run's transcript. It reuses agent.ReadTail — the same incremental-tail seam the
// TUI live view and `trau watch` use — so an in-place truncation on phase reuse
// surfaces as a reset frame rather than a corrupt stream.
func pumpTranscript(ctx context.Context, w io.Writer, flusher http.Flusher, resultsDir, pinned string, resume transcriptCursor, sinceNanos int64) {
	flusher.Flush()
	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()

	var curStem string
	var offset int64
	idle := 0
	for {
		wrote := false
		if path, stem := resolveTranscript(resultsDir, pinned, sinceNanos); path != "" {
			if stem != curStem {
				cols, rows := transcriptDims(path)
				if err := writeTranscriptMeta(w, stem, cols, rows); err != nil {
					return
				}
				curStem = stem
				offset = 0
				if resume.stem == stem {
					offset = resume.offset
				}
				flusher.Flush()
			}
			data, next, truncated := agent.ReadTail(path, offset)
			if truncated {
				if err := writeTranscriptReset(w); err != nil {
					return
				}
			}
			offset = next
			if len(data) > 0 {
				if err := writeTranscriptChunk(w, stem, offset, data); err != nil {
					return
				}
				flusher.Flush()
				wrote = true
			}
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

// resolveTranscript picks the transcript the stream follows this tick: the pinned
// id when given, else the newest under resultsDir at or after sinceNanos. An empty
// path means nothing to stream yet — the loop has not started a phase this run —
// so the connection idles open.
func resolveTranscript(resultsDir, pinned string, sinceNanos int64) (path, stem string) {
	if pinned != "" {
		p := filepath.Join(resultsDir, pinned+agent.TranscriptExt)
		if fileExists(p) {
			return p, pinned
		}
		return "", ""
	}
	p := newestTranscript(resultsDir, sinceNanos)
	if p == "" {
		return "", ""
	}
	return p, transcriptStem(p)
}

// listTranscripts reads every phase transcript under dir, newest first, marking
// the newest live. It never fails on a missing directory — a repo whose loop has
// not run yet simply has no transcripts.
func listTranscripts(dir string) []TranscriptView {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []TranscriptView{}
	}
	views := make([]TranscriptView, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), agent.TranscriptExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		cols, rows := transcriptDims(path)
		views = append(views, TranscriptView{
			ID:       transcriptStem(path),
			Label:    transcriptLabel(transcriptStem(path)),
			Cols:     cols,
			Rows:     rows,
			Size:     info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		return views[i].Modified > views[j].Modified
	})
	if len(views) > 0 {
		views[0].Live = true
	}
	return views
}

// newestTranscript returns the most-recently-modified transcript under dir, or ""
// when none exists — mirroring the resolution `trau watch` uses so the hub follows
// the same file. A non-zero sinceNanos excludes any session whose start (the
// unix-nano stem prefix) predates it, so follow mode never surfaces a run older
// than the caller's bound.
func newestTranscript(dir string, sinceNanos int64) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), agent.TranscriptExt) {
			continue
		}
		if sinceNanos > 0 {
			start, ok := transcriptStartNanos(strings.TrimSuffix(e.Name(), agent.TranscriptExt))
			if !ok || start < sinceNanos {
				continue
			}
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}

// transcriptStartNanos reads the session-start time encoded in a transcript stem,
// which the agent names <unix-nano>-<label>. It reports false when the stem has no
// leading nanosecond timestamp — a shape the since bound treats as out of range.
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
// unparseable is no bound, so a malformed since never blanks the stream.
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

func transcriptDims(path string) (cols, rows int) {
	if c, r, ok := agent.ReadSize(path); ok {
		return c, r
	}
	return defaultTranscriptCols, defaultTranscriptRows
}

func transcriptStem(path string) string {
	return strings.TrimSuffix(filepath.Base(path), agent.TranscriptExt)
}

// transcriptLabel recovers the phase label from a transcript stem, which the
// agent names <unix-nano>-<label>.
func transcriptLabel(stem string) string {
	if i := strings.IndexByte(stem, '-'); i >= 0 {
		return stem[i+1:]
	}
	return stem
}

func resultsDir(runsDir string) string {
	return filepath.Join(runsDir, agent.ResultsSubdir)
}

// resumeCursor reads the resume point from a browser reconnect's Last-Event-ID,
// whose frame id is <stem>:<offset>.
func resumeCursor(r *http.Request) transcriptCursor {
	id := r.Header.Get("Last-Event-ID")
	if id == "" {
		return transcriptCursor{}
	}
	i := strings.LastIndexByte(id, ':')
	if i < 0 {
		return transcriptCursor{}
	}
	off, err := strconv.ParseInt(id[i+1:], 10, 64)
	if err != nil || off < 0 {
		return transcriptCursor{}
	}
	return transcriptCursor{stem: id[:i], offset: off}
}

// validTranscriptID rejects a pinned id that could escape the agent-results
// directory. Legitimate stems are <unix-nano>-<sanitized-label>, so a single
// path component with no parent reference is the whole valid space.
func validTranscriptID(id string) bool {
	return id == filepath.Base(id) && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func writeTranscriptMeta(w io.Writer, stem string, cols, rows int) error {
	data, err := json.Marshal(transcriptMeta{ID: stem, Cols: cols, Rows: rows})
	if err != nil {
		return nil
	}
	_, err = fmt.Fprintf(w, "event: meta\ndata: %s\n\n", data)
	return err
}

func writeTranscriptReset(w io.Writer) error {
	_, err := io.WriteString(w, "event: reset\ndata: {}\n\n")
	return err
}

func writeTranscriptChunk(w io.Writer, stem string, offset int64, data []byte) error {
	_, err := fmt.Fprintf(w, "id: %s:%d\ndata: %s\n\n", stem, offset, base64.StdEncoding.EncodeToString(data))
	return err
}
