package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
)

func writeTranscript(t *testing.T, dir, name string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return p
}

func TestNewestTranscriptPicksNewestPtyLog(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	writeTranscript(t, dir, "1-build"+agent.TranscriptExt, base.Add(-2*time.Minute))
	want := writeTranscript(t, dir, "2-verify"+agent.TranscriptExt, base.Add(-1*time.Minute))
	// A newer file with a different extension must be ignored.
	writeTranscript(t, dir, "3-build.result.json", base)
	// A directory ending in the transcript extension must be skipped.
	if err := os.Mkdir(filepath.Join(dir, "stale"+agent.TranscriptExt), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := newestTranscript(dir); got != want {
		t.Errorf("newestTranscript = %q, want %q", got, want)
	}
}

func TestNewestTranscriptEmptyWhenNone(t *testing.T) {
	if got := newestTranscript(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Errorf("missing dir: got %q, want empty", got)
	}
	dir := t.TempDir()
	writeTranscript(t, dir, "1-build.result.json", time.Now())
	if got := newestTranscript(dir); got != "" {
		t.Errorf("no .pty.log: got %q, want empty", got)
	}
}

func TestTrimTrailingBlank(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want int
	}{
		{"trailing spaces and ansi", []string{"hi", "there", "   ", "\x1b[0m  \x1b[0m"}, 2},
		{"internal blanks kept", []string{"a", "", "b", ""}, 3},
		{"all blank", []string{"", "  ", "\x1b[0m"}, 0},
		{"none blank", []string{"a", "b"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimTrailingBlank(tc.in); len(got) != tc.want {
				t.Errorf("trimTrailingBlank(%q) len = %d, want %d", tc.in, len(got), tc.want)
			}
		})
	}
}

func TestTranscriptStem(t *testing.T) {
	got := transcriptStem(filepath.Join("/runs", "_agent-results", "171-build"+agent.TranscriptExt))
	if got != "171-build" {
		t.Errorf("transcriptStem = %q, want %q", got, "171-build")
	}
}
