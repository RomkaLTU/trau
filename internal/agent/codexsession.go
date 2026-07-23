package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// codexRolloutStats is the aggregate recovered from one interactive codex
// session rollout, mirroring what the --json event stream reports in exec mode.
type codexRolloutStats struct {
	Usage   Usage
	Turns   int    // model requests (one token_count event each)
	Model   string // last model the session's turns ran under
	Context int    // high-water mark: max input (incl. cache) over requests
}

// codexRolloutLine is the subset of a rollout line we read. session_meta carries
// the workspace, turn_context the model, and the token_count event_msg the
// running usage totals.
type codexRolloutLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type  string `json:"type"`
		Cwd   string `json:"cwd"`
		Model string `json:"model"`
		Info  *struct {
			Total codexTokenUsage `json:"total_token_usage"`
			Last  codexTokenUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type codexTokenUsage struct {
	Input     int `json:"input_tokens"`
	Cached    int `json:"cached_input_tokens"`
	Output    int `json:"output_tokens"`
	Reasoning int `json:"reasoning_output_tokens"`
}

// readCodexSessionStats recovers usage from the rollout codex wrote for the
// session started in dir at or after since. Interactive codex mints its own
// session id and never echoes it, so the rollout is identified by the workspace
// it recorded plus the call's start time rather than by an id trau chose. ok is
// false when nothing matches, so the caller can flag the call unrecovered instead
// of recording a false zero.
func readCodexSessionStats(sessionsDir, dir string, since time.Time) (codexRolloutStats, bool) {
	path, found := findCodexRollout(sessionsDir, dir, since)
	if !found {
		return codexRolloutStats{}, false
	}
	return readCodexRollout(path)
}

func readCodexRollout(path string) (codexRolloutStats, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexRolloutStats{}, false
	}
	defer func() { _ = f.Close() }()
	return parseCodexRollout(f)
}

// findCodexRollout returns the newest rollout under sessionsDir that codex wrote
// for dir no earlier than since. Codex files rollouts by date
// (<sessions>/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl).
func findCodexRollout(sessionsDir, dir string, since time.Time) (string, bool) {
	if sessionsDir == "" {
		return "", false
	}
	matches, _ := filepath.Glob(filepath.Join(sessionsDir, "*", "*", "*", "rollout-*.jsonl"))
	want := resolveWorkspace(dir)
	best, bestMod := "", time.Time{}
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil || info.ModTime().Before(since) || !info.ModTime().After(bestMod) {
			continue
		}
		if cwd, ok := rolloutWorkspace(path); !ok || resolveWorkspace(cwd) != want {
			continue
		}
		best, bestMod = path, info.ModTime()
	}
	return best, best != ""
}

func rolloutWorkspace(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var ln codexRolloutLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil || ln.Type != "session_meta" {
			continue
		}
		return ln.Payload.Cwd, ln.Payload.Cwd != ""
	}
	return "", false
}

// resolveWorkspace canonicalizes a workspace path for comparison: codex records
// the symlink-resolved cwd, which need not spell the same as the path trau
// handed the child (/tmp vs /private/tmp on macOS).
func resolveWorkspace(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// parseCodexRollout reads the session's token accounting. total_token_usage is
// cumulative for the session, so the final token_count event carries the whole
// call. Codex counts cached tokens inside input_tokens; the shared schema keeps
// Input non-cached, so the cached portion is moved to CacheRead. Malformed lines
// are skipped. ok is false when the session recorded no usage at all.
func parseCodexRollout(r io.Reader) (codexRolloutStats, bool) {
	var st codexRolloutStats
	var total codexTokenUsage
	any := false

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ln codexRolloutLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			continue
		}
		if ln.Type == "turn_context" {
			if ln.Payload.Model != "" {
				st.Model = ln.Payload.Model
			}
			continue
		}
		if ln.Payload.Type != "token_count" || ln.Payload.Info == nil {
			continue
		}
		any = true
		st.Turns++
		total = ln.Payload.Info.Total
		if ctx := ln.Payload.Info.Last.Input; ctx > st.Context {
			st.Context = ctx
		}
	}

	st.Usage = Usage{
		Input:     total.Input - total.Cached,
		Output:    total.Output,
		CacheRead: total.Cached,
		Reasoning: total.Reasoning,
	}
	return st, any
}
