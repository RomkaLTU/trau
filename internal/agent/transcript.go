package agent

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// transcriptStats is the aggregate recovered from one session transcript.
type transcriptStats struct {
	Usage   Usage
	Turns   int      // assistant API calls (one usage-bearing line each)
	Model   string   // last non-empty message.model seen
	Context int      // high-water mark: max(input+cache_read+cache_creation) over turns
	Skills  []string // skills loaded via the Skill tool, in first-seen order
}

// sessionLine is the subset of a transcript line we read. Only assistant lines
// carry usage/model; tool_use blocks inside their content name the skills loaded.
type sessionLine struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage *struct {
			Input         int `json:"input_tokens"`
			Output        int `json:"output_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Content []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				Skill string `json:"skill"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// newUUID returns a random RFC-4122 v4 UUID for `--session-id`. A fresh id per
// run guarantees a unique transcript filename, so reading it back is unambiguous.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// claudeConfigDir resolves Claude Code's config root: CLAUDE_CONFIG_DIR (first
// entry if it's a separator/comma list) else ~/.claude. Transcripts live under
// <root>/projects/.
func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		for _, sep := range []string{string(os.PathListSeparator), ","} {
			if i := strings.Index(d, sep); i >= 0 {
				d = d[:i]
			}
		}
		if d = strings.TrimSpace(d); d != "" {
			return d
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

// sessionTranscriptPath finds the transcript for sessionID. The session id is
// globally unique, so a glob across all project dirs finds it without having to
// reproduce Claude's cwd-escaping scheme.
func sessionTranscriptPath(configDir, sessionID string) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	return matches[0], true
}

// readSessionStats locates and parses the transcript for sessionID. ok is false
// when the file is missing or yields no usage-bearing line — callers leave the
// result un-enriched (the prior zero behavior) rather than failing.
func readSessionStats(configDir, sessionID string) (transcriptStats, bool) {
	path, found := sessionTranscriptPath(configDir, sessionID)
	if !found {
		return transcriptStats{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return transcriptStats{}, false
	}
	defer func() { _ = f.Close() }()
	return parseTranscript(f)
}

// parseTranscript sums usage across assistant lines, tracks the context
// high-water mark, records the model, and collects skills loaded via the Skill
// tool. Malformed lines are skipped. ok is false when no assistant line carried
// usage.
func parseTranscript(r interface{ Read([]byte) (int, error) }) (transcriptStats, bool) {
	var st transcriptStats
	seenSkill := map[string]bool{}
	any := false

	sc := bufio.NewScanner(bufio.NewReader(r))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var ln sessionLine
		if err := json.Unmarshal(b, &ln); err != nil || ln.Type != "assistant" {
			continue
		}
		if ln.Message.Model != "" {
			st.Model = ln.Message.Model
		}
		for _, blk := range ln.Message.Content {
			if blk.Type == "tool_use" && blk.Name == "Skill" && blk.Input.Skill != "" && !seenSkill[blk.Input.Skill] {
				seenSkill[blk.Input.Skill] = true
				st.Skills = append(st.Skills, blk.Input.Skill)
			}
		}
		if ln.Message.Usage == nil {
			continue
		}
		any = true
		st.Turns++
		u := ln.Message.Usage
		st.Usage.Input += u.Input
		st.Usage.Output += u.Output
		st.Usage.CacheRead += u.CacheRead
		st.Usage.CacheCreation += u.CacheCreation
		if ctx := u.Input + u.CacheRead + u.CacheCreation; ctx > st.Context {
			st.Context = ctx
		}
	}
	return st, any
}
