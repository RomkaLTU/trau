package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/vterm"
)

// liveStream polls an agent transcript from the hub into a virtual terminal so the
// Plan screen's w-attach view renders the planning agent legibly, the same live
// tail the running dashboard gives pipeline phases. It reuses the shared
// pollTranscript seam and streamDataMsg; a fresh planning round replaces the id and
// resets the emulator. attached toggles whether the view is shown; the tail keeps
// updating underneath either way so re-attaching lands on the current screen.
type liveStream struct {
	attached bool
	id       string
	cols     int
	rows     int
	seq      int64
	screen   *vterm.Screen
	reading  bool
}

// setID points the tail at a round's transcript session, emitted on the agent's
// KindAgentStart event. A repeat of the current id is ignored; a new id resets the
// emulator when attached so the fresh screen reconstructs from the top.
func (s *liveStream) setID(id string, cols, rows int) {
	if id == "" || id == s.id {
		return
	}
	s.id, s.cols, s.rows = id, cols, rows
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
	if s.screen == nil && s.id != "" {
		s.open()
	}
	return s.attached
}

func (s *liveStream) open() {
	if s.screen != nil {
		s.screen.Close()
	}
	s.screen = vterm.New(s.cols, s.rows)
	s.seq = -1
}

// reset tears the tail down between rounds, leaving nothing to render.
func (s *liveStream) reset() {
	if s.screen != nil {
		s.screen.Close()
	}
	s.screen = nil
	s.attached = false
	s.reading = false
	s.seq = -1
	s.id = ""
}

// write applies a transcript delta to the emulator. Deltas for a stale id are
// dropped.
func (s *liveStream) write(msg streamDataMsg) {
	s.reading = false
	if msg.id != s.id || s.screen == nil {
		return
	}
	s.screen.Write(msg.data)
	s.seq = msg.seq
}

// pump schedules the next transcript poll when attached and not already reading, so
// the emulator keeps up with the live agent between ticks.
func (s *liveStream) pump() tea.Cmd {
	if !s.attached || s.screen == nil || s.reading {
		return nil
	}
	s.reading = true
	return pollTranscript(s.id, s.seq)
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
