// Package console renders the loop's human-facing output: a colored, TTY-aware
// progress stream on stdout, plus a compact per-call stat line on stderr that
// replaces the raw JSON event blobs a human never wanted to read.
//
// It is the display counterpart to internal/event (which persists the
// machine-readable JSON stream to disk / the dashboard). Color is enabled only
// when stdout is a real terminal and NO_COLOR is unset (https://no-color.org);
// piped or redirected output stays plain, so stdout remains byte-stable and
// clean in captured logs.
package console

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
)

const (
	codeReset  = "\033[0m"
	codeDim    = "\033[2m"
	codeBold   = "\033[1m"
	codeRed    = "\033[31m"
	codeGreen  = "\033[32m"
	codeYellow = "\033[33m"
	codeBlue   = "\033[34m"
	codeCyan   = "\033[36m"
)

// Renderer is the narrow seam between the loop and its human-facing display.
// Both the plain console and the Bubble Tea TUI implement it. The plain Console
// implements the display-only hooks (SetTitle/PhaseStart/TicketDone) as no-ops —
// it already narrates per-phase and per-ticket inline — while the TUI uses them
// to drive the live pipeline stepper and the end-of-session summary.
type Renderer interface {
	Logf(format string, a ...any)
	LoopDone(s SessionSummary)
	Event(ev event.Event)
	Spin(phase string) (stop func())
	SetTicket(id string)
	SetTitle(title string)
	PhaseStart(phase string)
	TicketDone(r TicketResult)
	Wait()
}

// TicketResult is one ticket's outcome, assembled after its phases run. It feeds
// the TUI's per-ticket summary row and finalizes the live stepper. Fields are
// read from the durable state file + token sink, so it survives a resume.
type TicketResult struct {
	ID     string
	Title  string
	Phase  string
	Branch string
	PRURL  string
	Tokens int
	Cost   float64
	// CostMetered is false when some phase logged tokens but no per-call dollar
	// cost (a kimi/codex subscription call), so Cost is a lower bound, not a
	// measured total.
	CostMetered bool
	Elapsed     time.Duration
}

// SessionSummary is the loop's closing totals, handed to LoopDone once the run
// ends (cleanly or aborted). Err is non-nil when the loop bailed before
// processing anything (e.g. a dirty base) so the TUI can show why instead of
// vanishing.
type SessionSummary struct {
	Tickets     int
	TotalTokens int
	TotalCost   float64
	// CostMetered is false when any processed phase logged tokens but no per-call
	// dollar cost (kimi/codex subscription phases), so TotalCost is a lower bound.
	CostMetered bool
	Elapsed     time.Duration
	Err         error
	Paused      bool // loop stopped on a blameless provider rate/usage limit
	// Fault is set when the loop stopped on an UNEXPECTED error (agent crash,
	// failed push, infra hiccup) partway through a ticket. The ticket's WIP was
	// preserved on its branch without quarantining or filing a bug, so it stays
	// resumable. FaultID/FaultPhase name the ticket and the phase it died in.
	Fault      bool
	FaultID    string
	FaultPhase string
}

// Console writes timestamped progress to out (stdout) and per-call stat lines to
// err (stderr). Splitting the streams keeps stdout byte-stable when not a TTY,
// while the stat lines (and any errors) stay on stderr.
type Console struct {
	out, err io.Writer
	color    bool
	now      func() time.Time

	spinMu   sync.Mutex
	spinStop chan struct{}
	spinDone chan struct{}
}

// New returns a Console over out/err, enabling color only when out is a terminal.
func New(out, err io.Writer) *Console {
	return &Console{out: out, err: err, color: colorEnabled(out), now: time.Now}
}

// WithClock overrides the timestamp source; intended for deterministic tests.
func (c *Console) WithClock(now func() time.Time) *Console { c.now = now; return c }

// IsTerminal reports whether w is a character device (i.e. an interactive
// terminal). It ignores CLICOLOR_FORCE/NO_COLOR; callers that care about color
// policy should use colorEnabled.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("CLICOLOR_FORCE") != "" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (c *Console) paint(code, s string) string {
	if !c.color || code == "" {
		return s
	}
	return code + s + codeReset
}

// Logf prints a timestamped progress line to stdout. In plain mode the output is
// exactly "HH:MM:SS <msg>\n", the stable format dry-run / status output depends
// on. In color mode the timestamp is dimmed and the message is tinted by its
// leading glyph.
func (c *Console) Logf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	ts := c.now().Format("15:04:05")
	if !c.color {
		_, _ = fmt.Fprintf(c.out, "%s %s\n", ts, msg)
		return
	}
	_, _ = fmt.Fprintf(c.out, "%s  %s\n", c.paint(codeDim, ts), c.tint(msg))
}

func (c *Console) tint(msg string) string {
	switch t := strings.TrimLeft(msg, " "); {
	case strings.HasPrefix(t, "▸"), strings.HasPrefix(t, "▶"):
		return c.paint(codeCyan, msg)
	case strings.HasPrefix(t, "✓"), strings.HasPrefix(t, "✔"):
		return c.paint(codeGreen, msg)
	case strings.HasPrefix(t, "✗"):
		return c.paint(codeRed, msg)
	case strings.HasPrefix(t, "⚠"):
		return c.paint(codeYellow, msg)
	case strings.HasPrefix(t, "↻"), strings.HasPrefix(t, "↳"):
		return c.paint(codeDim, msg)
	case strings.HasPrefix(t, "==="):
		return c.paint(codeBold, msg)
	case strings.HasPrefix(t, "→"), strings.HasPrefix(t, "PR "):
		return c.paint(codeBlue, msg)
	default:
		return msg
	}
}

// Event renders a structured event for humans on stderr. Only agent_call is
// shown today — a dim, indented one-liner of model/effort/context/duration plus
// the skills loaded — so the terminal carries the signal that used to arrive as a
// raw JSON blob. Other kinds are ignored here (they live in the JSON stream).
// Persisting is event.Log's job; this is display only, and it writes to stderr so
// stdout stays byte-stable. (num_turns and total_tokens are recorded in the JSON
// stream but not shown — the running total is confusing here because it includes
// cache reads and does not reflect billed spend.)
func (c *Console) Event(ev event.Event) {
	if ev.Kind != "agent_call" {
		return
	}
	c.stopSpinner()
	parts := make([]string, 0, 4)
	if tag := modelTag(ev.Fields); tag != "" {
		parts = append(parts, tag)
	}
	if ctx := intField(ev.Fields, "context_tokens"); ctx > 0 {
		parts = append(parts, fmtTokens(ctx)+" ctx")
	}
	parts = append(parts, fmtDur(intField(ev.Fields, "duration_ms")))
	if skills := stringsField(ev.Fields, "skills"); len(skills) > 0 {
		parts = append(parts, "skills: "+strings.Join(skills, ", "))
	}
	body := strings.Join(parts, " · ")
	if isErr, _ := ev.Fields["is_error"].(bool); isErr {
		body = "error · " + body
		_, _ = fmt.Fprintln(c.err, c.paint(codeRed, "              ↳ "+body))
		return
	}
	_, _ = fmt.Fprintln(c.err, c.paint(codeDim, "              ↳ "+body))
}

// LoopDone renders the loop's closing line. Plain mode prints the exact
// "loop finished (N ticket(s) processed)"; color mode prints a green one-line
// recap with the notional cost and wall-clock elapsed. The cost is rendered
// "~$X est" because interactive providers do not report billed cost; it is a
// NOTIONAL API-list estimate (most of the underlying token total is cache reads
// priced at 0.1×; see internal/tokens).
func (c *Console) LoopDone(s SessionSummary) {
	if s.Err != nil {
		return
	}
	if !c.color {
		c.Logf("loop finished (%d ticket(s) processed)", s.Tickets)
		return
	}
	noun := "tickets"
	if s.Tickets == 1 {
		noun = "ticket"
	}
	c.Logf("✔ done · %d %s · %s · %s",
		s.Tickets, noun, costSummary(s.TotalCost, s.CostMetered), fmtDur(int(s.Elapsed.Milliseconds())))
}

// costSummary renders the closing total honestly: "~$X est" (a notional list
// estimate) when every call was metered, "cost n/a" when no dollar cost was
// measured at all (a kimi/codex-only run), and "~$X+ est" when the figure is a
// lower bound because some calls were unmetered.
func costSummary(cost float64, metered bool) string {
	if metered {
		return "~$" + strconv.FormatFloat(cost, 'f', -1, 64) + " est"
	}
	if cost == 0 {
		return "cost n/a"
	}
	return "~$" + strconv.FormatFloat(cost, 'f', -1, 64) + "+ est"
}

// Spin shows a live progress indicator for a phase on stderr while a (blocking)
// agent call runs, so the operator can tell work is in flight vs hung. It returns
// a stop function that erases the line; call it when the phase returns. It is a
// no-op when color is off (not a TTY) so piped/redirected output stays byte-clean
// — the phase's "▸ build" line and the closing stat line already mark progress
// there. The braille frames advance every 100ms with the elapsed time.
//
// The spinner registers itself on the Console so Event (the per-call stat line,
// emitted from inside the agent call while the spinner is still running) can
// stop+erase it before printing — otherwise the two would interleave on stderr.
func (c *Console) Spin(phase string) (stop func()) {
	if !c.color {
		return func() {}
	}
	c.stopSpinner()
	spinStop := make(chan struct{})
	done := make(chan struct{})
	c.spinMu.Lock()
	c.spinStop, c.spinDone = spinStop, done
	c.spinMu.Unlock()

	go func() {
		defer close(done)
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		start := c.now()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-spinStop:
				_, _ = fmt.Fprint(c.err, "\r\033[K")
				return
			case <-t.C:
				el := fmtDur(int(c.now().Sub(start) / time.Millisecond))
				_, _ = fmt.Fprintf(c.err, "\r%s %s %s",
					c.paint(codeCyan, string(frames[i%len(frames)])), phase, c.paint(codeDim, el))
				i++
			}
		}
	}()
	return func() { c.stopSpinner() }
}

// SetTicket is a no-op for the plain console; it exists to satisfy Renderer.
func (c *Console) SetTicket(id string) {}

// SetTitle is a no-op for the plain console; the ticket id already anchors the
// per-phase log lines, so the title adds nothing in plain mode.
func (c *Console) SetTitle(title string) {}

// PhaseStart is a no-op for the plain console; each phase already prints its own
// "▸ <phase>" progress line, so the stepper signal is redundant here.
func (c *Console) PhaseStart(phase string) {}

// TicketDone is a no-op for the plain console; per-ticket outcomes are already
// narrated inline (merged/quarantined lines) as they happen.
func (c *Console) TicketDone(r TicketResult) {}

// Wait is a no-op for the plain console; it exists to satisfy Renderer.
func (c *Console) Wait() {}

func (c *Console) stopSpinner() {
	c.spinMu.Lock()
	stop, done := c.spinStop, c.spinDone
	c.spinStop, c.spinDone = nil, nil
	c.spinMu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

func modelTag(f map[string]any) string {
	model := shortModel(strField(f, "model"))
	effort := strField(f, "effort")
	switch {
	case model != "" && effort != "":
		return model + " @" + effort
	case model != "":
		return model
	case effort != "":
		return "@" + effort
	default:
		return ""
	}
}

func shortModel(model string) string {
	return strings.TrimPrefix(model, "claude-")
}

func strField(f map[string]any, k string) string {
	s, _ := f[k].(string)
	return s
}

func stringsField(f map[string]any, k string) []string {
	switch v := f[k].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func intField(f map[string]any, k string) int {
	switch v := f[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 2, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.Itoa(n)
	}
}

func fmtDur(ms int) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds()+0.5)) + "s"
	default:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	}
}
