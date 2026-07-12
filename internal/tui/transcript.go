package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

// transcriptPollTimeout bounds one live-view poll so a slow or unreachable hub
// never stalls the TUI's tick cadence.
const transcriptPollTimeout = 2 * time.Second

// TranscriptDelta is the live agent view's read of the hub's transcript chunk
// store: the resolved session id, its terminal dimensions, the bytes appended
// since the caller's cursor, and the seq to resume from.
type TranscriptDelta struct {
	ID   string
	Cols int
	Rows int
	Data []byte
	Seq  int64
}

// TranscriptSource fetches live transcript chunks from the hub for the live agent
// view, replacing the file tail (ADR 0008 §4). The main package wires a hub-backed
// source at startup; a nil source leaves the live pane on its placeholder.
type TranscriptSource interface {
	Chunks(ctx context.Context, repo, id string, after int64) (TranscriptDelta, error)
}

// liveTranscript and liveTranscriptRepo are the process-wide source the live view
// polls, set once at startup like screenRepo. One TUI runs per process.
var (
	liveTranscript     TranscriptSource
	liveTranscriptRepo string
)

// SetTranscriptSource wires the hub-backed transcript source the live agent view
// polls, scoped to repo. Called once at startup before the program runs.
func SetTranscriptSource(src TranscriptSource, repo string) {
	liveTranscript = src
	liveTranscriptRepo = repo
}

// pollTranscript reads the next transcript delta for session id after seq and wraps
// it as the emulator message. With no source wired it returns an empty delta at the
// unchanged cursor, so the live pane idles rather than errors.
func pollTranscript(id string, after int64) tea.Cmd {
	return func() tea.Msg {
		if liveTranscript == nil || id == "" {
			return streamDataMsg{id: id, seq: after}
		}
		ctx, cancel := context.WithTimeout(context.Background(), transcriptPollTimeout)
		defer cancel()
		d, err := liveTranscript.Chunks(ctx, liveTranscriptRepo, id, after)
		if err != nil {
			return streamDataMsg{id: id, seq: after}
		}
		return streamDataMsg{id: d.ID, seq: d.Seq, cols: d.Cols, rows: d.Rows, data: d.Data}
	}
}
