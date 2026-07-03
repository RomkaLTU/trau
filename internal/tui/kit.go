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
