package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/RomkaLTU/trau/internal/event"
)

// TestReadTailStripsANSIAndAdvancesOffset checks a delta read returns only the
// bytes appended since offset, ANSI/CR-stripped, advancing the offset.
func TestReadTailStripsANSIAndAdvancesOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase.pty.log")
	if err := os.WriteFile(path, []byte("\x1b[31mhello\x1b[0m\r\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first := readTail(path, 0)
	if first.path != path {
		t.Fatalf("path = %q, want %q", first.path, path)
	}
	if strings.Contains(first.chunk, "\x1b") || strings.Contains(first.chunk, "\r") {
		t.Errorf("chunk still carries escape/CR bytes: %q", first.chunk)
	}
	if first.chunk != "hello\nworld\n" {
		t.Errorf("chunk = %q, want %q", first.chunk, "hello\nworld\n")
	}

	// A read at the advanced offset with nothing new appended is a no-op.
	if again := readTail(path, first.offset); again.chunk != "" {
		t.Errorf("expected empty delta at EOF, got %q", again.chunk)
	}

	// Append more; the next read returns only the new bytes.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("again\n")
	_ = f.Close()
	if d := readTail(path, first.offset); d.chunk != "again\n" {
		t.Errorf("delta = %q, want %q", d.chunk, "again\n")
	}
}

// TestReadTailMissingFileIsNoOp checks a not-yet-created transcript is a no-op.
func TestReadTailMissingFileIsNoOp(t *testing.T) {
	got := readTail(filepath.Join(t.TempDir(), "absent.pty.log"), 7)
	if got.chunk != "" || got.offset != 7 {
		t.Errorf("missing file: got chunk=%q offset=%d, want empty chunk at offset 7", got.chunk, got.offset)
	}
}

// TestIngestStreamBuffersLinesBounded checks line splitting, partial-line holding,
// and the maxLogLines cap.
func TestIngestStreamBuffersLinesBounded(t *testing.T) {
	var m model
	m.ingestStream("alpha\nbeta\npart")
	if got := strings.Join(m.streamLines, ","); got != "alpha,beta" {
		t.Errorf("lines = %q, want %q", got, "alpha,beta")
	}
	if m.streamTail != "part" {
		t.Errorf("tail = %q, want %q", m.streamTail, "part")
	}
	m.ingestStream("ial\n")
	if last := m.streamLines[len(m.streamLines)-1]; last != "partial" {
		t.Errorf("partial line not stitched: last = %q", last)
	}
	if m.streamTail != "" {
		t.Errorf("tail should be drained, got %q", m.streamTail)
	}

	var big strings.Builder
	for i := 0; i < maxLogLines*2; i++ {
		big.WriteString("x\n")
	}
	m.ingestStream(big.String())
	if len(m.streamLines) > maxLogLines {
		t.Errorf("buffer unbounded: %d lines, cap is %d", len(m.streamLines), maxLogLines)
	}
}

// TestApplyEventAgentStartRepoints checks agent_start re-points to a new transcript
// and resets the offset/buffer, while a repeat of the same path does not.
func TestApplyEventAgentStartRepoints(t *testing.T) {
	m := initialModel(nil)
	m.applyEvent(event.Event{Kind: event.KindAgentStart, Fields: map[string]any{"transcript_path": "/runs/_agent-results/1-build.pty.log"}})
	if m.streamPath != "/runs/_agent-results/1-build.pty.log" {
		t.Fatalf("streamPath = %q, want the build transcript", m.streamPath)
	}

	m.streamOffset = 42
	m.streamLines = []string{"stale"}
	m.applyEvent(event.Event{Kind: event.KindAgentStart, Fields: map[string]any{"transcript_path": m.streamPath}})
	if m.streamOffset != 42 || len(m.streamLines) != 1 {
		t.Errorf("same path must not reset: offset=%d lines=%d", m.streamOffset, len(m.streamLines))
	}

	m.applyEvent(event.Event{Kind: event.KindAgentStart, Fields: map[string]any{"transcript_path": "/runs/_agent-results/2-verify.pty.log"}})
	if m.streamOffset != 0 || len(m.streamLines) != 0 || m.streamPath != "/runs/_agent-results/2-verify.pty.log" {
		t.Errorf("new phase must re-point + reset, got path=%q offset=%d lines=%d", m.streamPath, m.streamOffset, len(m.streamLines))
	}
}

// TestRenderStreamPlaceholderAndContent checks the placeholder with no transcript
// and the buffered tail once one is active.
func TestRenderStreamPlaceholderAndContent(t *testing.T) {
	m := initialModel(nil)
	d := m.dims()

	if out := m.renderStream(d); !strings.Contains(out, "claude only") {
		t.Errorf("no-transcript pane must show the placeholder, got:\n%s", out)
	}

	m.streamPath = "/runs/_agent-results/1-build.pty.log"
	m.streamLines = []string{"running tests"}
	if out := m.renderStream(d); !strings.Contains(out, "running tests") {
		t.Errorf("active pane must show buffered output, got:\n%s", out)
	}
}

// TestWatchKeyTogglesStreaming checks w flips the live view on and off.
func TestWatchKeyTogglesStreaming(t *testing.T) {
	m := initialModel(nil)
	w := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}

	m, _, handled := m.handleKey(w)
	if !handled || !m.streaming {
		t.Fatalf("first w must enable streaming (handled=%v streaming=%v)", handled, m.streaming)
	}
	m, _, handled = m.handleKey(w)
	if !handled || m.streaming {
		t.Fatalf("second w must disable streaming (handled=%v streaming=%v)", handled, m.streaming)
	}
}
