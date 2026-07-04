package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/state"
)

// This file is the glance → peek → attach interaction over the queue rail. Peek
// (space) floats a read-only preview of the selected row over the still-rendering
// dashboard; attach (enter on the live row, or w) drops into the full-screen live
// agent view. Both are read-only, so unlike the recovery verbs they stay available
// while the loop runs.

const (
	peekMinRows = 3
	peekMaxRows = 20
)

// peeking reports whether the preview overlay is open, so the app shell routes
// every key to it while it owns input.
func (m model) peeking() bool { return m.peek }

// attachTarget reports whether enter attaches a row's live view rather than
// peeking it. Only the active ticket streams a live transcript, so only it is
// attachable — the single source of the attach-vs-peek decision.
func attachTarget(sel QueueRow) bool { return sel.Live }

// canPeek reports whether a row can be previewed now: the rail must be drawn (so a
// selection is visible and we aren't already attached) and a row must be selected.
func (m model) canPeek() bool {
	if !m.railVisible() {
		return false
	}
	_, ok := m.selectedRow()
	return ok
}

// attach opens the full-screen live agent view for the active transcript — the
// shared target of w, enter on the live row, and enter from the peek layer. It
// starts the tail emulator when it isn't already following the current phase.
func (m model) attach() (model, tea.Cmd, bool) {
	m.streaming = true
	if m.stream == nil && m.streamPath != "" {
		m.startStream()
		m.streamReading = true
		return m, m.tailReadCmd(), true
	}
	return m, nil, true
}

// streamPaneTitle frames the attached live view so both the attached state and the
// way back out read straight off the pane border.
func (m model) streamPaneTitle() string {
	return "◉ Attached · " + m.streamLabel() + " — esc detach"
}

// handlePeekKey drives the preview overlay: esc/space/q close it, ↑↓ move the rail
// selection so the preview follows the queue, and enter attaches when the selected
// row is the live ticket. Every key is swallowed so nothing fires underneath.
func (m model) handlePeekKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "space", "q":
		m.peek = false
		return m, nil, true
	case "up", "k":
		m.moveQueueCursor(-1)
		return m, nil, true
	case "down", "j":
		m.moveQueueCursor(1)
		return m, nil, true
	case "enter":
		if sel, ok := m.selectedRow(); ok && attachTarget(sel) {
			m.peek = false
			return m.attach()
		}
		return m, nil, true
	}
	return m, nil, true
}

// peekContent selects the preview title and body for one queue row by its state:
// the live tail for the active ticket, the failure reason for a quarantined or
// faulted one, the PR summary for a merged one, and a short status otherwise. It
// reads only the row and the model's live tail, so the same glance renders
// wherever a row is selected.
func (m model) peekContent(sel QueueRow, bodyW, maxRows int) (title string, body []string) {
	switch {
	case sel.Live:
		title = sel.ID + "  " + firstNonEmpty(m.activePhase(), "running")
		body = m.peekTail(m.phaseTailLines(activeIndex(m.steps), maxRows), bodyW)
	case sel.Phase == state.Merged:
		// A ticket that recovered from an earlier transient failure keeps a stale
		// FailureReason, so the terminal-success states are matched before it.
		title = sel.ID + "  merged"
		body = m.peekMerged(sel, bodyW)
	case sel.Phase == phaseReset:
		title = sel.ID + "  reset"
		body = []string{m.styles.Subtle.Render("Working tree restored; nothing to resume.")}
	case sel.Phase == state.Quarantined || sel.FailureReason != "":
		title = sel.ID + "  " + queueDesc(sel)
		body = m.peekFailure(sel, bodyW, maxRows)
	case sel.Phase == "":
		title = sel.ID + "  ready"
		body = []string{m.styles.Subtle.Render("Queued — not started yet.")}
	default:
		title = sel.ID + "  " + prettyPhase(sel.Phase)
		body = []string{m.styles.Subtle.Render("In progress — " + prettyPhase(sel.Phase) + ".")}
		if sel.Branch != "" {
			body = append(body, m.peekMeta("branch", sel.Branch, bodyW))
		}
	}
	if len(body) == 0 {
		body = []string{m.styles.Subtle.Render("No output captured yet.")}
	}
	if t := strings.TrimSpace(sel.Title); t != "" {
		body = append([]string{m.styles.Help.Render(truncate(t, bodyW)), ""}, body...)
	}
	return title, body
}

// peekTail styles nothing new — the tail lines already carry glyph colors — it just
// clamps each to the card width.
func (m model) peekTail(lines []string, bodyW int) []string {
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		out = append(out, ansi.Truncate(ln, bodyW, "…"))
	}
	return out
}

// peekFailure surfaces the preserved failure reason (word-wrapped) with the branch
// and PR beneath, so a quarantined ticket explains itself without opening Logs.
func (m model) peekFailure(sel QueueRow, bodyW, maxRows int) []string {
	reason := oneLine(sel.FailureReason)
	if reason == "" {
		reason = "Failed — open logs for detail."
	}
	wrapped := strings.Split(m.styles.Warning.Width(bodyW).Render(reason), "\n")
	if budget := maxRows - 2; budget >= 1 && len(wrapped) > budget {
		wrapped = wrapped[:budget]
	}
	out := wrapped
	if sel.Branch != "" {
		out = append(out, m.peekMeta("branch", sel.Branch, bodyW))
	}
	if sel.PRURL != "" {
		out = append(out, m.peekMeta("PR", sel.PRURL, bodyW))
	}
	return out
}

// peekMerged shows the closing summary for a merged ticket: its PR and the tokens
// and cost it spent.
func (m model) peekMerged(sel QueueRow, bodyW int) []string {
	out := []string{m.styles.Success.Render("✓ merged")}
	if sel.PRURL != "" {
		out = append(out, m.peekMeta("PR", sel.PRURL, bodyW))
	}
	if sel.Tokens > 0 || sel.Cost > 0 {
		out = append(out, m.styles.Help.Render(fmtTokens(sel.Tokens)+" tokens · "+costSummaryTUI(sel.Cost, sel.CostMetered)))
	}
	return out
}

// peekMeta renders a "label value" metadata row, the value truncated to fit.
func (m model) peekMeta(label, value string, bodyW int) string {
	return m.styles.Help.Render(label+" ") + m.styles.Subtle.Render(truncate(value, bodyW-len(label)-1))
}

// renderPeekPanel builds the floating preview card for the selected row, or "" when
// nothing is selected. The border and centering mirror the ? help overlay.
func (m model) renderPeekPanel(w, hgt int) string {
	sel, ok := m.selectedRow()
	if !ok {
		return ""
	}
	innerW := w - 8
	if innerW > 96 {
		innerW = 96
	}
	if innerW < 24 {
		innerW = 24
	}
	bodyW := innerW - 2

	maxRows := hgt - 10
	if maxRows < peekMinRows {
		maxRows = peekMinRows
	}
	if maxRows > peekMaxRows {
		maxRows = peekMaxRows
	}

	title, body := m.peekContent(sel, bodyW, maxRows)

	nav := "esc close"
	if attachTarget(sel) {
		nav = "enter attach · " + nav
	}
	if m.selectableCount() > 1 {
		nav = "↑↓ next · " + nav
	}

	lines := []string{m.styles.Header.Render(truncate(title, innerW)), ""}
	lines = append(lines, body...)
	lines = append(lines, "", m.styles.Help.Render(nav))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Brand).
		Padding(0, 1).
		Width(innerW).
		Render(strings.Join(lines, "\n"))
}

// compositePeek floats the preview card centered over the running dashboard.
func (m model) compositePeek(base string) string {
	panel := m.renderPeekPanel(m.width, m.height)
	if panel == "" {
		return base
	}
	return centerOverlay(base, panel, m.width, m.height)
}
