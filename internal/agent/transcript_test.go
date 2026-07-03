package agent

import (
	"os"
	"path/filepath"
	"testing"
)

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
