package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/vterm"
)

// liveStream tails an agent PTY transcript into a virtual terminal so the Plan
// screen's w-attach view renders the planning agent legibly, the same live tail
// the running dashboard gives pipeline phases. It reuses the shared readTail seam
// and streamDataMsg; a fresh planning round replaces the path and resets the
// emulator. attached toggles whether the view is shown; the tail keeps updating
// underneath either way so re-attaching lands on the current screen.
type liveStream struct {
	attached bool
	path     string
	cols     int
	rows     int
	offset   int64
	screen   *vterm.Screen
	reading  bool
}

// setPath points the tail at a round's transcript, emitted on the agent's
// KindAgentStart event. A repeat of the current path is ignored; a new path resets
// the emulator when attached so the fresh screen reconstructs from the top.
func (s *liveStream) setPath(path string, cols, rows int) {
	if path == "" || path == s.path {
		return
	}
	s.path, s.cols, s.rows = path, cols, rows
	if s.attached {
		s.open()
	}
}

// toggle flips the attached view, opening the emulator on first attach, and
// reports whether it is now attached.
func (s *liveStream) toggle() bool {
	if s.attached {
		s.attached = false
		return false
	}
	s.attached = true
	if s.screen == nil && s.path != "" {
		s.open()
	}
	return s.attached
}

func (s *liveStream) open() {
	if s.screen != nil {
		s.screen.Close()
	}
	s.screen = vterm.New(s.cols, s.rows)
	s.offset = 0
}

// reset tears the tail down between rounds, leaving nothing to render.
func (s *liveStream) reset() {
	if s.screen != nil {
		s.screen.Close()
	}
	s.screen = nil
	s.attached = false
	s.reading = false
	s.offset = 0
	s.path = ""
}

// write applies a transcript delta to the emulator, resetting the screen first
// when the file was truncated (a reused transcript). Deltas for a stale path are
// dropped.
func (s *liveStream) write(msg streamDataMsg) {
	s.reading = false
	if msg.path != s.path || s.screen == nil {
		return
	}
	if msg.truncated {
		s.screen.Close()
		s.screen = vterm.New(s.cols, s.rows)
	}
	s.screen.Write(msg.data)
	s.offset = msg.offset
}

// pump schedules the next transcript read when attached and not already reading,
// so the emulator keeps up with the live agent between ticks.
func (s *liveStream) pump() tea.Cmd {
	if !s.attached || s.screen == nil || s.reading {
		return nil
	}
	s.reading = true
	path, offset := s.path, s.offset
	return func() tea.Msg { return readTail(path, offset) }
}

// view renders the current screen clipped to w×h; empty until the first delta.
func (s *liveStream) view(w, h int) string {
	if s.screen == nil {
		return ""
	}
	lines := s.screen.Lines()
	if h > 0 && len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], w, "")
	}
	return strings.Join(lines, "\n")
}
