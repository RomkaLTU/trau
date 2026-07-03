package tui

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/notify"
	"github.com/RomkaLTU/trau/internal/sanitize"
)

// TUI is a Bubble Tea-backed renderer. It implements console.Renderer and runs
// the TUI program on the alternate screen.
type TUI struct {
	prog *tea.Program
	done chan struct{}
}

// New starts the Bubble Tea program on stdout and returns a TUI renderer.
// onInterrupt is invoked the first time the user asks to quit during a run, so
// the loop can stop gracefully and the program can show its summary instead of
// being killed mid-flight; pass nil to disable graceful stop.
func New(stdout, _ io.Writer, onInterrupt func(), notifyOn bool) *TUI {
	m := initialModel(onInterrupt)
	if notifyOn {
		m.notifier = notify.OS()
	}
	zone.NewGlobal()
	prog := tea.NewProgram(m, tea.WithOutput(stdout))
	t := &TUI{prog: prog, done: make(chan struct{})}
	go func() {
		_, _ = prog.Run()
		close(t.done)
	}()
	return t
}

// NewRenderer returns a renderer whose Bubble Tea program is attached later by
// RunSession. It lets the main package wire this renderer into the event log
// (so agent stat lines reach the TUI) before the program exists.
func NewRenderer() *TUI {
	return &TUI{done: make(chan struct{})}
}

// RunSession runs the persistent menu shell to completion (blocking). The shell
// owns the whole session — menu, read-only views, and the live dashboard — so
// selecting an action no longer tears the TUI down. holder is the renderer the
// loop reports through; its program is attached here.
func RunSession(ctx context.Context, stdout io.Writer, holder *TUI, actions Actions) error {
	m := newAppModel(ctx, actions, holder)
	zone.NewGlobal()
	prog := tea.NewProgram(m, tea.WithOutput(stdout))
	holder.prog = prog
	_, err := prog.Run()
	return err
}

// Logf appends a formatted log line to the TUI. The line is sanitized to a single
// clean row (ANSI/control chars stripped, bounded length) so raw subprocess output
// — a hook's \r progress bars or multi-line ANSI — can't escape the row and repaint
// over other panels.
func (t *TUI) Logf(format string, a ...any) {
	if t == nil || t.prog == nil {
		return
	}
	t.prog.Send(logMsg{line: sanitize.FeedLine(fmt.Sprintf(format, a...))})
}

// Event forwards a structured event to the TUI for display.
func (t *TUI) Event(ev event.Event) {
	if t == nil || t.prog == nil {
		return
	}
	t.prog.Send(eventMsg{ev: ev})
}

// Spin is a no-op for the TUI: the live pipeline stepper already animates its
// active step, so a separate spinner signal is redundant. Kept to satisfy
// console.Renderer.
func (t *TUI) Spin(phase string) (stop func()) { return func() {} }

// SetTicket tells the TUI which ticket is now being processed.
func (t *TUI) SetTicket(id string) {
	if t == nil || t.prog == nil {
		return
	}
	t.prog.Send(ticketMsg{id: id})
}

// SetTitle updates the current ticket's human-readable title.
func (t *TUI) SetTitle(title string) {
	if t == nil || t.prog == nil {
		return
	}
	t.prog.Send(titleMsg{title: title})
}

// PhaseStart advances the pipeline stepper to the named phase.
func (t *TUI) PhaseStart(phase string) {
	if t == nil || t.prog == nil {
		return
	}
	t.prog.Send(phaseStartMsg{phase: phase})
}

// TicketDone records a ticket's outcome for the end-of-session summary.
func (t *TUI) TicketDone(r console.TicketResult) {
	if t == nil || t.prog == nil {
		return
	}
	t.prog.Send(ticketDoneMsg{r: r})
}

// LoopDone flips the TUI to its completion summary. It does not shut the program
// down — the recap stays on screen until the user dismisses it.
func (t *TUI) LoopDone(s console.SessionSummary) {
	if t == nil || t.prog == nil {
		return
	}
	t.prog.Send(loopDoneMsg{s: s})
}

// Wait blocks until the Bubble Tea program has shut down and the terminal is
// restored. The program exits when the user dismisses the summary (or force-
// quits), so callers MUST send LoopDone before Wait — otherwise there is nothing
// to dismiss and this blocks forever.
func (t *TUI) Wait() {
	if t == nil {
		return
	}
	<-t.done
}
