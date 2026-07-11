package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseTranscriptSumsSubagentUsage guards the token-accounting requirement for
// the Explore opt-in: a run that spawns read-only subagents logs their assistant
// turns into the same session transcript with isSidechain=true, and those turns
// must be summed into the phase's usage total, not dropped.
func TestParseTranscriptSumsSubagentUsage(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}}`,
		`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-4-8","usage":{"input_tokens":40,"output_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`,
	}, "\n")

	st, ok := parseTranscript(strings.NewReader(transcript))
	if !ok {
		t.Fatal("parseTranscript reported no usage-bearing lines")
	}
	if st.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (main + subagent)", st.Turns)
	}
	if st.Usage.Input != 140 {
		t.Errorf("Input = %d, want 140 (100 main + 40 subagent)", st.Usage.Input)
	}
	if st.Usage.Output != 30 {
		t.Errorf("Output = %d, want 30 (20 main + 10 subagent)", st.Usage.Output)
	}
	if st.Usage.CacheRead != 7 {
		t.Errorf("CacheRead = %d, want 7", st.Usage.CacheRead)
	}
	if st.Usage.CacheCreation != 4 {
		t.Errorf("CacheCreation = %d, want 4", st.Usage.CacheCreation)
	}
}

// TestReadTailIncrementalDelta checks the shared seam returns only the bytes
// appended since offset, advances the offset, and is an EOF no-op once caught up.
func TestReadTailIncrementalDelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase.pty.log")
	if err := os.WriteFile(path, []byte("\x1b[31mhello\x1b[0m"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, next, truncated := ReadTail(path, 0)
	if string(data) != "\x1b[31mhello\x1b[0m" || truncated {
		t.Fatalf("first read = %q truncated=%v, want raw bytes unchanged", data, truncated)
	}

	if d, n, _ := ReadTail(path, next); len(d) != 0 || n != next {
		t.Fatalf("caught-up read = %q offset=%d, want empty no-op at %d", d, n, next)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\x1b[2Jmore")
	_ = f.Close()
	d, n, truncated := ReadTail(path, next)
	if string(d) != "\x1b[2Jmore" || truncated {
		t.Fatalf("delta = %q truncated=%v, want only the new bytes", d, truncated)
	}
	if n != next+int64(len("\x1b[2Jmore")) {
		t.Errorf("offset = %d, want it advanced by the delta length", n)
	}
}

// TestReadTailTruncationRestarts checks that a file which shrank below offset (an
// in-place phase reuse) restarts from the top and flags truncation so the caller
// can reset its emulator.
func TestReadTailTruncationRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase.pty.log")
	if err := os.WriteFile(path, []byte("first-frame-lots-of-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, offset, _ := ReadTail(path, 0)

	if err := os.WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, next, truncated := ReadTail(path, offset)
	if !truncated {
		t.Errorf("shrunk file must report truncated so the emulator resets")
	}
	if string(data) != "new" || next != int64(len("new")) {
		t.Errorf("restart read = %q offset=%d, want the whole new file from 0", data, next)
	}
}

// TestReadTailMissingFileIsNoOp checks a not-yet-created transcript leaves the
// offset untouched — watch may point at a phase before its file exists.
func TestReadTailMissingFileIsNoOp(t *testing.T) {
	data, next, truncated := ReadTail(filepath.Join(t.TempDir(), "absent.pty.log"), 7)
	if len(data) != 0 || next != 7 || truncated {
		t.Errorf("missing file: got data=%q offset=%d truncated=%v, want empty at 7", data, next, truncated)
	}
}

// TestReadTailEmptyPathIsNoOp checks the empty-path guard (no active transcript).
func TestReadTailEmptyPathIsNoOp(t *testing.T) {
	if data, next, truncated := ReadTail("", 3); len(data) != 0 || next != 3 || truncated {
		t.Errorf("empty path: got data=%q offset=%d truncated=%v, want empty at 3", data, next, truncated)
	}
}
