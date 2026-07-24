package agent

import (
	"context"
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

// spawnEnv is the environment every agent child process is launched with. ctx may
// carry per-call overrides (e.g. browser-harness recording for a verify run).
func spawnEnv(ctx context.Context) []string {
	env := ScrubClaudeSessionEnv(os.Environ())
	if browserRecording(ctx) {
		env = append(env, "BH_RECORD=1")
	}
	return env
}

type browserRecordKey struct{}

// WithBrowserRecording marks ctx so the child spawned under it records its
// browser-harness session (BH_RECORD=1). It is a per-process override for the one
// verify run; it never touches the user's global recordings preference.
func WithBrowserRecording(ctx context.Context) context.Context {
	return context.WithValue(ctx, browserRecordKey{}, true)
}

func browserRecording(ctx context.Context) bool {
	on, _ := ctx.Value(browserRecordKey{}).(bool)
	return on
}
