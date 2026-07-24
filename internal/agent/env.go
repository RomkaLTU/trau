package agent

import (
	"os"
	"strings"
)

// ScrubClaudeSessionEnv strips the session markers a host Claude Code instance
// stamps on every process it spawns (CLAUDE_CODE_*, CLAUDECODE, CLAUDE_PID).
// Agent runs are deliberate top-level sessions: a claude child that inherits
// CLAUDE_CODE_CHILD_SESSION treats itself as a nested subagent and disables
// transcript saving, which blinds the transcript-backed activity view and
// breaks resume. trau's own CLAUDE_* config keys (CLAUDE_MODEL, CLAUDE_EFFORT,
// CLAUDE_BIN, ...) do not match these prefixes and pass through.
func ScrubClaudeSessionEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CODE_") ||
			strings.HasPrefix(kv, "CLAUDECODE=") ||
			strings.HasPrefix(kv, "CLAUDE_PID=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// spawnEnv is the environment every agent child process is launched with.
func spawnEnv() []string { return ScrubClaudeSessionEnv(os.Environ()) }
