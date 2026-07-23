package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// kimiSessionStats is the aggregate recovered from one interactive kimi session,
// mirroring what print mode recovers from the session id echoed on stdout.
type kimiSessionStats struct {
	Usage Usage
	Turns int
	Model string
}

// readKimiSessionStats recovers a session's usage through the same wire.jsonl
// reader print mode uses, so both kimi paths account identically. ok is false when
// the session recorded none, so the caller can flag the call unrecovered instead
// of recording a false zero.
func readKimiSessionStats(sessionsDir, sessionID string) (kimiSessionStats, bool) {
	u, turns, model, ok := readKimiUsage(sessionsDir, sessionID)
	return kimiSessionStats{Usage: u, Turns: turns, Model: model}, ok
}

// kimiSessionIndex is the file kimi appends one line to per session, beside its
// sessions dir, naming the session and the workspace it was started in.
const kimiSessionIndex = "session_index.jsonl"

type kimiIndexEntry struct {
	SessionID  string `json:"sessionId"`
	SessionDir string `json:"sessionDir"`
	WorkDir    string `json:"workDir"`
}

// findKimiSession returns the id of the session kimi started in dir at or after
// since. Interactive kimi mints its own session id and never echoes it — print
// mode's session.resume_hint has no equivalent on a TUI — so the session is
// identified by the workspace the index recorded plus the call's start time.
// Entries are appended in start order, so the last match is the newest.
func findKimiSession(sessionsDir, dir string, since time.Time) (string, bool) {
	if sessionsDir == "" || dir == "" {
		return "", false
	}
	f, err := os.Open(filepath.Join(filepath.Dir(sessionsDir), kimiSessionIndex))
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	want := resolveWorkspace(dir)
	var found kimiIndexEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e kimiIndexEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.SessionID == "" {
			continue
		}
		if resolveWorkspace(e.WorkDir) == want {
			found = e
		}
	}
	if found.SessionID == "" {
		return "", false
	}
	// A workspace trau has run before has older sessions indexed under it too; only
	// the one this call started has been written to since the call began.
	info, err := os.Stat(found.SessionDir)
	if err != nil || info.ModTime().Before(since) {
		return "", false
	}
	return found.SessionID, true
}
