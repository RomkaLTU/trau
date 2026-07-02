// Package sanitize neutralizes raw subprocess output before it reaches a sink that
// assumes clean, single-line text — a TUI feed row or a KEY=value checkpoint line.
// Tool output (hook runners, linters, progress bars) carries ANSI color codes, \r
// redraws, and embedded newlines that escape a feed row and repaint over other
// panels, or split one state value across several lines that then reparse as bogus
// keys. It is a leaf package (stdlib only) so any layer can depend on it.
package sanitize

import "regexp"

var reANSI = regexp.MustCompile("\x1b\\[[0-9;:?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?")

// StripANSI removes ANSI escape/control sequences from s.
func StripANSI(s string) string { return reANSI.ReplaceAllString(s, "") }

// reControlRun matches runs of C0 control characters (including \n, \r, \t) and
// DEL. Ordinary spaces (0x20) are deliberately left alone so intentional feed
// indentation and word spacing survive.
var reControlRun = regexp.MustCompile("[\x00-\x1f\x7f]+")

// oneLine strips ANSI then folds every run of control characters into a single
// space, yielding one line with no embedded newlines/carriage-returns/escape
// codes. It does not trim or collapse ordinary spaces, so a caller's leading
// indentation and alignment are preserved.
func oneLine(s string) string {
	return reControlRun.ReplaceAllString(StripANSI(s), " ")
}

const (
	feedMax  = 200
	stateMax = 1024
)

// FeedLine makes subprocess output safe to render as a single TUI feed row: one
// line, no ANSI/control chars, bounded length. When truncated it keeps a short
// head (which command) and the informative tail (the summary tools print last,
// e.g. "husky - pre-push script failed (code 1)"), eliding the noisy middle.
func FeedLine(s string) string { return truncate(oneLine(s), feedMax) }

// StateValue makes s safe to persist as one KEY=value line: single line, no
// control chars, bounded so a runaway blob can't bloat the checkpoint file. Full
// raw output belongs in a run log, not the resumable state.
func StateValue(s string) string { return truncate(oneLine(s), stateMax) }

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	head := max / 4
	tail := max - head - 1
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
