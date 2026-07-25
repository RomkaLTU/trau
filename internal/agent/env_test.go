package agent

import (
	"context"
	"reflect"
	"slices"
	"testing"
)

func TestChildEnv(t *testing.T) {
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
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
	}
	got := ChildEnv(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChildEnv() = %v, want %v", got, want)
	}
	if again := ChildEnv(got); !reflect.DeepEqual(again, want) {
		t.Errorf("ChildEnv() re-applied = %v, want %v", again, want)
	}
}

func TestBrowserRecordingEnv(t *testing.T) {
	if plain := spawnEnv(context.Background()); slices.Contains(plain, "BH_RECORD=1") {
		t.Fatalf("spawnEnv set BH_RECORD without WithBrowserRecording")
	}
	if rec := spawnEnv(WithBrowserRecording(context.Background())); !slices.Contains(rec, "BH_RECORD=1") {
		t.Fatalf("spawnEnv under WithBrowserRecording did not set BH_RECORD=1")
	}
}
