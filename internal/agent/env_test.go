package agent

import (
	"reflect"
	"testing"
)

func TestScrubClaudeSessionEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CODE_SESSION_ID=1a07bb69",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		"CLAUDECODE=1",
		"CLAUDE_PID=70106",
		"CLAUDE_MODEL=opus",
		"CLAUDE_EFFORT=high",
		"CLAUDE_BIN=claude",
		"CLAUDECODEC=keep", // prefix near-miss must survive
		"TRAU_ACTIVE=1",    // not this helper's concern
	}
	want := []string{
		"PATH=/usr/bin",
		"CLAUDE_MODEL=opus",
		"CLAUDE_EFFORT=high",
		"CLAUDE_BIN=claude",
		"CLAUDECODEC=keep",
		"TRAU_ACTIVE=1",
	}
	if got := ScrubClaudeSessionEnv(in); !reflect.DeepEqual(got, want) {
		t.Errorf("ScrubClaudeSessionEnv() = %v, want %v", got, want)
	}
}
