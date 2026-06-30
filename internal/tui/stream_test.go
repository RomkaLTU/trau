package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/RomkaLTU/trau/internal/event"
)

// TestReadTailReturnsRawDelta checks a delta read returns the raw bytes appended
// since offset (unmodified, for the emulator) and advances the offset.
func TestReadTailReturnsRawDelta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase.pty.log")
	if err := os.WriteFile(path, []byte("\x1b[31mhello\x1b[0m"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := readTail(path, 0)
	if string(first.data) != "\x1b[31mhello\x1b[0m" {
		t.Errorf("data = %q, want raw bytes unchanged", first.data)
	}
	if again := readTail(path, first.offset); len(again.data) != 0 {
		t.Errorf("expected empty delta at EOF, got %q", again.data)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("\x1b[2Jmore")
	_ = f.Close()
	if d := readTail(path, first.offset); string(d.data) != "\x1b[2Jmore" {
		t.Errorf("delta = %q, want only the new bytes", d.data)
	}
}

// TestReadTailMissingFileIsNoOp checks a not-yet-created transcript is a no-op.
func TestReadTailMissingFileIsNoOp(t *testing.T) {
	got := readTail(filepath.Join(t.TempDir(), "absent.pty.log"), 7)
	if len(got.data) != 0 || got.offset != 7 {
		t.Errorf("missing file: got data=%q offset=%d, want empty at offset 7", got.data, got.offset)
	}
}

// TestApplyEventAgentStartTracksPath checks agent_start records the active
// transcript path and updates it when a new phase opens a different file.
func TestApplyEventAgentStartTracksPath(t *testing.T) {
	m := initialModel(nil)
	m.applyEvent(event.Event{Kind: event.KindAgentStart, Fields: map[string]any{"transcript_path": "/runs/1-build.pty.log"}})
	if m.streamPath != "/runs/1-build.pty.log" {
		t.Fatalf("streamPath = %q, want the build transcript", m.streamPath)
	}
	m.applyEvent(event.Event{Kind: event.KindAgentStart, Fields: map[string]any{"transcript_path": "/runs/2-verify.pty.log"}})
	if m.streamPath != "/runs/2-verify.pty.log" {
		t.Errorf("new phase must re-point, got %q", m.streamPath)
	}
}

// TestWatchKeyTogglesStream checks w opens a live screen when a transcript is
// known and tears it down on the second press, never touching the loop.
func TestWatchKeyTogglesStream(t *testing.T) {
	m := initialModel(nil)
	m.streamPath = "/runs/1-build.pty.log"
	w := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}}

	m, _, handled := m.handleKey(w)
	if !handled || !m.streaming || m.stream == nil {
		t.Fatalf("first w must open the live screen (handled=%v streaming=%v stream=%v)", handled, m.streaming, m.stream != nil)
	}
	m, _, handled = m.handleKey(w)
	if !handled || m.streaming || m.stream != nil {
		t.Fatalf("second w must close it (handled=%v streaming=%v stream=%v)", handled, m.streaming, m.stream != nil)
	}
}

// TestRenderStreamPlaceholder checks the pane shows the live-view placeholder
// when no live screen is active.
func TestRenderStreamPlaceholder(t *testing.T) {
	m := initialModel(nil)
	if out := m.renderStream(m.dims()); !strings.Contains(out, "live agent view") {
		t.Errorf("no-stream pane must show the placeholder, got:\n%s", out)
	}
}
