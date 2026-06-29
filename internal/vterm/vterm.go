// Package vterm reconstructs a legible screen from an agent's raw PTY transcript.
//
// Claude Code paints a full-screen alt-screen TUI with cursor addressing and no
// newlines, so naively stripping or appending its bytes yields garbage. This
// feeds the bytes through a virtual terminal emulator and renders the resulting
// screen at the geometry the agent's PTY painted at.
package vterm

import (
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/x/vt"
)

const (
	defaultCols = 80
	defaultRows = 24
)

// Screen is a virtual terminal fed raw transcript bytes via Write and rendered to
// styled lines via Lines. Close releases it.
type Screen struct {
	emu     *vt.Emulator
	stop    atomic.Bool
	drained chan struct{}
}

// New returns a started Screen sized to the agent's terminal geometry.
// Non-positive dimensions fall back to 80x24, the size Claude paints at when its
// PTY is unsized.
func New(cols, rows int) *Screen {
	if cols <= 0 {
		cols = defaultCols
	}
	if rows <= 0 {
		rows = defaultRows
	}
	s := &Screen{emu: vt.NewEmulator(cols, rows), drained: make(chan struct{})}
	go s.drain()
	return s
}

// drain discards the terminal's replies (device-attribute reports, status
// queries) so Write never blocks on the unread input pipe. It owns the emulator's
// teardown so Read and Close never run concurrently.
func (s *Screen) drain() {
	defer close(s.drained)
	buf := make([]byte, 256)
	for {
		_, err := s.emu.Read(buf)
		if s.stop.Load() {
			_ = s.emu.Close()
			return
		}
		if err != nil {
			return
		}
	}
}

// Write feeds transcript bytes into the emulator.
func (s *Screen) Write(p []byte) {
	if len(p) > 0 {
		_, _ = s.emu.Write(p)
	}
}

// Lines renders the current screen as styled rows (SGR preserved).
func (s *Screen) Lines() []string {
	return strings.Split(s.emu.Render(), "\n")
}

// Close stops the emulator and waits for its drain goroutine to exit.
func (s *Screen) Close() {
	if s.stop.Swap(true) {
		return
	}
	_, _ = s.emu.InputPipe().Write([]byte{0}) // unblock the drain's Read
	<-s.drained
}
