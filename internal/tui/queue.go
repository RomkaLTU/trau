package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// QueueRow is one ticket in the attention-sorted queue rail. It is the single
// row model shared by the three surfaces that show the run's tickets: the live
// dashboard rail, the session-complete recap, and the standalone Status browse
// screen. Whoever builds the rows fills the display-ready fields (Desc, Age);
// the semantic fields (Phase, FailureReason, Branch, PRURL) drive sorting,
// folding, and which recovery verbs apply.
type QueueRow struct {
	ID            string
	Title         string
	Phase         string // a state phase constant, phaseReset, or "" when not started
	PRURL         string
	Branch        string
	FailureReason string
	// FailureClass is the checkpoint's classified failure (state.FailPaused /
	// FailStopped / FailFaulted / FailGaveUp), or "" when the row has no failure. It
	// drives which recovery the Handle confirm offers per class — resume vs the
	// destructive reset.
	FailureClass string
	Tokens       int
	Cost         float64
	CostMetered  bool

	// Age is the row's measured elapsed (recap) or time-since-last-update
	// (live/browse). Live marks the actively-running ticket so its glyph
	// animates. Desc overrides the phase-derived state description for the live
	// row (its precise active phase); empty means derive from Phase.
	Age  time.Duration
	Live bool
	Desc string
}

// attention buckets a row by how much it needs a human, lowest first. The rail
// sorts by this so quarantined/faulted work floats to the top and finished work
// folds away at the bottom.
type attention int

const (
	attnNeedsHuman attention = iota // quarantined or faulted (a preserved failure)
	attnInFlight                    // a live or resumable non-terminal phase
	attnReady                       // planned, no checkpoint yet
	attnDone                        // merged or reset
)

func (r QueueRow) attention() attention {
	switch {
	case r.Phase == state.Merged || r.Phase == phaseReset:
		return attnDone
	case r.Live:
		return attnInFlight
	case r.Phase == state.Quarantined || r.FailureReason != "":
		return attnNeedsHuman
	case r.Phase == "":
		return attnReady
	default:
		return attnInFlight
	}
}

// recoverableRow reports whether a row still has work to act on — neither merged
// nor already reset — so resume/checkout/reset make sense. Mirrors recoverable()
// for the QueueRow shape.
func recoverableRow(r QueueRow) bool {
	return r.Phase != state.Merged && r.Phase != phaseReset
}

// sortQueue orders rows by attention bucket, floating the live ticket to the top
// of its bucket, then by ID for stability.
func sortQueue(rows []QueueRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if ai, aj := rows[i].attention(), rows[j].attention(); ai != aj {
			return ai < aj
		}
		if rows[i].Live != rows[j].Live {
			return rows[i].Live
		}
		return rows[i].ID < rows[j].ID
	})
}

// partitionQueue sorts rows and splits them into the selectable rows and the
// finished rows that fold to a summary line. foldDone controls whether merged/
// reset rows fold away (the live rail, to keep the focus on active work) or stay
// selectable (the recap and Status, where opening a merged ticket's PR matters).
func partitionQueue(rows []QueueRow, foldDone bool) (active, done []QueueRow) {
	sorted := make([]QueueRow, len(rows))
	copy(sorted, rows)
	sortQueue(sorted)
	for _, r := range sorted {
		if foldDone && r.attention() == attnDone {
			done = append(done, r)
		} else {
			active = append(active, r)
		}
	}
	return active, done
}

// queueVerbs reports which recovery verbs apply to r. live is true during an
// active loop run, where every tree/loop/store-mutating verb (resume, checkout,
// reset) is withheld — reset force-switches the working tree off the running
// branch (Pipeline.Reset), so mid-run it would corrupt the active ticket. Only
// the read-only verbs (open, logs) stay live; the rest act from the recap/Status.
func queueVerbs(r QueueRow, live bool) (open, logs, resume, branch, reset bool) {
	rec := recoverableRow(r)
	open = r.PRURL != ""
	logs = true
	// Quarantined is terminal (rank 9): pipeline.Resume would run zero phases, so
	// resume is a no-op trap. Withhold it here so Status and the recap footers agree
	// with the Handle confirm's reset-only rule for quarantined.
	resume = rec && !live && r.Phase != state.Quarantined
	branch = rec && r.Branch != "" && !live
	reset = rec && !live && !r.Live
	return open, logs, resume, branch, reset
}

// needsAttention reports whether the row carries a classified failure — the
// single predicate the main-menu Handle item and the attention buckets share.
func (r QueueRow) needsAttention() bool { return r.FailureClass != "" }

// canResume reports whether the Handle confirm should offer Resume for this row's
// failure class: paused (re-run after the human clears the block), stopped, and
// faulted (resume from the preserved checkpoint). Quarantined (gave_up) is
// terminal.
func (r QueueRow) canResume() bool {
	switch r.FailureClass {
	case state.FailPaused, state.FailStopped, state.FailFaulted:
		return true
	default:
		return false
	}
}

// canReset reports whether the Handle confirm should offer the destructive Reset:
// faulted and quarantined (gave_up). Paused and stopped are blameless and are
// never offered a Reset — the human clears the block, or restarts the run, then
// resumes.
func (r QueueRow) canReset() bool {
	return r.FailureClass == state.FailFaulted || r.FailureClass == state.FailGaveUp
}

// failureLabel is the short class name shown on the menu Handle item and the
// confirm header: quarantined reads friendlier than its stored "gave_up".
func failureLabel(class string) string {
	switch class {
	case state.FailPaused:
		return "paused"
	case state.FailStopped:
		return "stopped"
	case state.FailGaveUp:
		return "quarantined"
	default:
		return "faulted"
	}
}

// attentionGlyph is the class-colored mark for the menu Handle item, matching the
// rail: ⏸ for a blameless paused/stopped row, ⚠ for faulted/quarantined.
func attentionGlyph(s Styles, class string) (string, lipgloss.Style) {
	if class == state.FailPaused || class == state.FailStopped {
		return "⏸", s.Warning
	}
	return "⚠", s.Error
}

// queueVerbHints lists the applicable recovery-verb hints for the selected row,
// shared by every queue footer (rail, recap, Status) so the wording can't drift.
func queueVerbHints(sel QueueRow, hasSel, live bool) []string {
	if !hasSel {
		return nil
	}
	open, logs, resume, branch, reset := queueVerbs(sel, live)
	var parts []string
	if open {
		parts = append(parts, "o open")
	}
	if logs {
		parts = append(parts, "l logs")
	}
	if resume {
		parts = append(parts, "r resume")
	}
	if branch {
		parts = append(parts, "b branch")
	}
	if reset {
		parts = append(parts, "x reset")
	}
	return parts
}

// queueHint builds the one-line key legend for the selected row, listing only the
// verbs that apply. A pending reset swaps in the two-key confirm prompt. Used by
// the Status screen (reconcile is reachable, so R trails the verbs).
func queueHint(sel QueueRow, hasSel, live bool, confirmID string) string {
	if confirmID != "" {
		return "⚠ reset " + confirmID + "? x again to confirm · esc cancel"
	}
	parts := append([]string{"↑↓ move"}, queueVerbHints(sel, hasSel, live)...)
	parts = append(parts, "R reconcile")
	return strings.Join(markVerbs(parts), " · ")
}

// queueRowGlyph is the state indicator drawn before the ticket id: the live
// spinner frame for the running ticket, else a phase-colored mark.
func queueRowGlyph(s Styles, spinFrame string, r QueueRow) string {
	if r.Live {
		return s.StepActive.Render(spinFrame)
	}
	glyph, style := queueGlyph(s, r)
	return style.Render(glyph)
}

func queueGlyph(s Styles, r QueueRow) (string, lipgloss.Style) {
	switch r.attention() {
	case attnNeedsHuman:
		if r.Phase == state.Quarantined {
			return "⚠", s.Error
		}
		return "▲", s.Warning
	case attnReady:
		return "○", s.StepPending
	case attnDone:
		if r.Phase == phaseReset {
			return "↺", s.Subtle
		}
		return "✓", s.Success
	default: // in-flight, not live
		return "◔", s.Info
	}
}

// queueDesc is the one-line state description shown after the id. The live row
// carries its precise phase in Desc; others derive it from the stored phase.
func queueDesc(r QueueRow) string {
	if r.Desc != "" {
		return r.Desc
	}
	switch r.attention() {
	case attnNeedsHuman:
		if r.Phase == state.Quarantined {
			return "quarantined"
		}
		return "needs attention"
	case attnReady:
		return "ready"
	case attnDone:
		if r.Phase == phaseReset {
			return "reset"
		}
		return "merged"
	default:
		return prettyPhase(r.Phase)
	}
}

// prettyPhase turns a stored phase constant into a human label: pr_open → "PR
// open", handed_off → "handed off".
func prettyPhase(phase string) string {
	switch phase {
	case "":
		return "queued"
	case state.PROpen:
		return "PR open"
	default:
		return strings.ReplaceAll(phase, "_", " ")
	}
}

// renderQueue is the shared rail body: the selectable rows in attention order
// (the cursor row highlighted, its failure reason revealed beneath it), then the
// finished rows folded to one summary line. width is the inner text width; when
// height > 0 the rows window around the cursor so a long queue scrolls. spinFrame
// is the current spinner glyph animating any live row.
func renderQueue(s Styles, spinFrame string, rows []QueueRow, cursor, width, height int, foldDone bool, zonePrefix string) string {
	if width < 8 {
		width = 8
	}
	active, done := partitionQueue(rows, foldDone)
	if len(active) == 0 && len(done) == 0 {
		return s.Subtle.Render("no tracked tickets")
	}

	var lines []string
	anchor := 0
	for i, r := range active {
		focused := i == cursor
		if focused {
			anchor = len(lines)
		}
		line := queueRowLine(s, spinFrame, r, focused, width)
		if zonePrefix != "" {
			line = zone.Mark(zonePrefix+r.ID, line)
		}
		lines = append(lines, line)
		// Needs-human rows always surface their reason so a failure is never
		// hidden behind the cursor; any other reason-bearing row shows it too.
		// Terminal-success rows suppress it — a merged ticket may carry a stale
		// reason from an earlier attempt that was resolved outside trau.
		if r.FailureReason != "" && recoverableRow(r) {
			lines = append(lines, s.Subtle.Render("    "+truncate("↳ "+oneLine(r.FailureReason), width-4)))
		}
	}
	if line := foldedDoneLine(s, done, width); line != "" {
		lines = append(lines, line)
	}

	if height > 0 {
		lines = scrollToCursor(lines, anchor, height)
	}
	return strings.Join(lines, "\n")
}

// queueRowLine renders "▸ ◔ COD-1  building  1m" for one row, truncated to width.
func queueRowLine(s Styles, spinFrame string, r QueueRow, focused bool, width int) string {
	idStyle := s.Subtle
	if focused {
		idStyle = s.Header
	}
	left := cursorMarker(s, focused) + queueRowGlyph(s, spinFrame, r) + " " +
		idStyle.Render(r.ID) + "  " + s.Subtle.Render(queueDesc(r))
	if r.Age > 0 {
		left += "  " + s.Help.Render(fmtDur(r.Age))
	}
	return ansi.Truncate(left, width, "…")
}

// foldedDoneLine collapses the finished rows to "✓ 2 merged · 1 reset". Empty
// when nothing finished.
func foldedDoneLine(s Styles, done []QueueRow, width int) string {
	merged, reset := 0, 0
	for _, r := range done {
		if r.Phase == phaseReset {
			reset++
		} else {
			merged++
		}
	}
	var parts []string
	if merged > 0 {
		parts = append(parts, s.Success.Render(fmt.Sprintf("✓ %d merged", merged)))
	}
	if reset > 0 {
		parts = append(parts, s.Subtle.Render(fmt.Sprintf("↺ %d reset", reset)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ansi.Truncate(strings.Join(parts, "  "), width, "…")
}

// oneLine flattens whitespace/newlines in a failure reason to a single line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
