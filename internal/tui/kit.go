package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// This file is the shared component kit: one implementation each of the visual
// widgets every screen draws with. Screens compose these rather than hand-roll
// their own container, cursor, or radio, so the UI reads as one language and
// later redesign slices change one place.

// titledPanel draws a rounded box of total width w and height h with the title
// woven into the top border. body is pre-rendered (and may carry ANSI); its lines
// are padded/truncated to the inner width and the block to h-2 rows. It is the
// container for the tiled dashboard/logs panes.
func titledPanel(s Styles, title, body string, w, h int) string {
	if w < 6 {
		w = 6
	}
	textW := w - 4
	innerH := h - 2
	if innerH < 1 {
		innerH = 1
	}
	title = truncate(title, w-5)
	fill := w - 5 - lipgloss.Width(title)
	if fill < 0 {
		fill = 0
	}
	border := s.Separator
	top := border.Render("╭─ ") + s.PaneTitle.Render(title) + border.Render(" "+strings.Repeat("─", fill)+"╮")
	bottom := border.Render("╰" + strings.Repeat("─", w-2) + "╯")
	bar := border.Render("│")

	lines := strings.Split(body, "\n")
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	lines = lines[:innerH]

	out := make([]string, 0, innerH+2)
	out = append(out, top)
	for _, ln := range lines {
		out = append(out, bar+" "+pad(ln, textW)+" "+bar)
	}
	out = append(out, bottom)
	return strings.Join(out, "\n")
}

// cardBox renders body inside the shared rounded card, hard-capped to maxW so it
// never overflows a narrow terminal. body is pre-rendered and may open with a
// SummaryTitle heading.
func cardBox(s Styles, maxW int, body string) string {
	return s.SummaryCard.MaxWidth(maxW).Render(body)
}

// centerScreen stacks blocks vertically and centers the stack in the w×h
// viewport — the standard full-screen layout for card screens.
func centerScreen(w, h int, blocks ...string) string {
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, blocks...))
}

// hintBar renders the one-line footer legend shown beneath card screens.
func hintBar(s Styles, hint string) string {
	return s.Help.Render(hint)
}

// cardView is the common card screen: a card centered over the viewport with a
// hint bar beneath it.
func cardView(s Styles, w, h int, body, hint string) string {
	return centerScreen(w, h, cardBox(s, w, body), hintBar(s, hint))
}

// titledCardView is cardView with a SummaryTitle heading prepended to body.
func titledCardView(s Styles, w, h int, title, body, hint string) string {
	return cardView(s, w, h, s.SummaryTitle.Render(title)+"\n\n"+body, hint)
}

// cursorMarker is the leading glyph of a selectable row: a pointer when focused,
// two blank cells otherwise. The single source of the ▸ cursor idiom.
func cursorMarker(s Styles, focused bool) string {
	if focused {
		return s.Info.Render("▸ ")
	}
	return "  "
}

// listRow renders a selectable "▸ label   desc" row. When focused the marker
// shows and the label/description brighten. label is padded to labelW so
// descriptions align down the column; an empty desc renders just marker+label
// (labelW is then ignored).
func listRow(s Styles, focused bool, label, desc string, labelW int) string {
	labelStyle, descStyle := s.Subtle, s.Help
	if focused {
		labelStyle, descStyle = s.Header, s.Subtle
	}
	row := cursorMarker(s, focused) + labelStyle.Render(label)
	if desc != "" {
		gap := labelW - lipgloss.Width(label)
		if gap < 1 {
			gap = 1
		}
		row += strings.Repeat(" ", gap) + descStyle.Render(desc)
	}
	return row
}

// radioRow renders a single-choice picker as "● chosen   ○ other", the selected
// option highlighted. The single source of the radio idiom.
func radioRow(s Styles, labels []string, idx int) string {
	parts := make([]string, len(labels))
	for i, label := range labels {
		if i == idx {
			parts[i] = s.Header.Render("● " + label)
		} else {
			parts[i] = s.Help.Render("○ " + label)
		}
	}
	return strings.Join(parts, "   ")
}

// cardBodyBudget returns how many body lines fit inside a card screen on an
// h-row terminal: total height minus the card's border and padding (4) and the
// hint bar (1), minus `extra` fixed rows stacked above the card (e.g. a brand
// header). Never less than 1.
func cardBodyBudget(h, extra int) int {
	b := h - 5 - extra
	if b < 1 {
		b = 1
	}
	return b
}

// cardMaxWidth caps card growth on very wide terminals so a centered card keeps
// comfortable margins and readable line lengths.
const cardMaxWidth = 100

// cardContentWidth is a card's inner content width on a termW-wide terminal:
// the width (capped at cardMaxWidth) minus the border and padding. It depends
// only on the terminal size, never on the content, so a line laid out to it
// fixes the card width and a centered card never shifts as its content changes.
func cardContentWidth(termW int) int {
	w := termW
	if w > cardMaxWidth {
		w = cardMaxWidth
	}
	w -= 6 // 1-cell border + 2-cell padding on each side
	if w < 1 {
		w = 1
	}
	return w
}

// descReserve is the number of rows to reserve for a description footer: the
// tallest wrap height among descs at the given width, clamped to [1, maxLines].
func descReserve(descs []string, width, maxLines int) int {
	n := 1
	for _, d := range descs {
		if d == "" {
			continue
		}
		if h := lipgloss.Height(lipgloss.Wrap(d, width, "")); h > n {
			n = h
		}
	}
	if n > maxLines {
		n = maxLines
	}
	return n
}

// descBlock lays a description out as exactly `lines` rows, word-wrapped to
// width and each row padded to width (short text blank-padded, overflow
// dropped), so the block's width and height are constant regardless of the text.
func descBlock(style lipgloss.Style, desc string, width, lines int) []string {
	if width < 1 {
		width = 1
	}
	if lines < 1 {
		lines = 1
	}
	var wrapped []string
	if desc != "" {
		wrapped = strings.Split(lipgloss.Wrap(desc, width, ""), "\n")
	}
	out := make([]string, lines)
	for i := range out {
		text := ""
		if i < len(wrapped) {
			text = wrapped[i]
		}
		out[i] = style.Render(pad(text, width))
	}
	return out
}

// scrollToCursor windows lines to at most height rows, edge-scrolling so the row
// at index anchor stays visible. Content that already fits is returned unchanged,
// so a terminal tall enough to show everything looks identical. This is the
// selection-list scroll primitive: callers pass the focused row's line index.
func scrollToCursor(lines []string, anchor, height int) []string {
	if height < 1 {
		height = 1
	}
	if len(lines) <= height {
		return lines
	}
	start := 0
	if anchor >= height {
		start = anchor - height + 1
	}
	if limit := len(lines) - height; start > limit {
		start = limit
	}
	if start < 0 {
		start = 0
	}
	return lines[start : start+height]
}

// windowAt windows lines to at most height rows starting at offset, clamping the
// offset into range. It returns the visible slice, the clamped offset, and
// whether the content overflowed (so callers can show a scroll affordance).
// Content that fits is returned unchanged with offset 0. This is the prose scroll
// primitive: callers keep the offset as state driven by scroll keys/mouse wheel.
func windowAt(lines []string, offset, height int) (window []string, clamped int, overflow bool) {
	if height < 1 {
		height = 1
	}
	if len(lines) <= height {
		return lines, 0, false
	}
	maxOff := len(lines) - height
	if offset > maxOff {
		offset = maxOff
	}
	if offset < 0 {
		offset = 0
	}
	return lines[offset : offset+height], offset, true
}
