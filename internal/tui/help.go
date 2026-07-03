package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// This file is the shared help system. Every screen declares its key bindings
// once as a screenHelp; that single value drives both the compact footer legend
// (footer()) and the floating "?" overlay (compositeHelp). Deriving both from
// one declaration is what keeps the footer, the overlay, and real behavior from
// drifting apart.

// helpKey is one key binding as shown to the user. key is the display label
// (e.g. "↑↓", "enter", "⇥"); desc says what it does. footer marks bindings that
// also belong in the one-line footer legend beneath the screen.
type helpKey struct {
	key    string
	desc   string
	footer bool
}

// fk declares a binding shown in BOTH the footer legend and the ? overlay.
func fk(key, desc string) helpKey { return helpKey{key: key, desc: desc, footer: true} }

// xk declares a binding shown ONLY in the ? overlay — the "extra" keys the
// one-line footer has no room for.
func xk(key, desc string) helpKey { return helpKey{key: key, desc: desc} }

// helpColumn groups related bindings under a heading in the overlay.
type helpColumn struct {
	title string
	keys  []helpKey
}

// group builds a helpColumn; a small constructor to keep screen declarations terse.
func group(title string, keys ...helpKey) helpColumn { return helpColumn{title: title, keys: keys} }

// screenHelp is a screen's complete key legend and the single source of truth
// for its footer hint and its ? overlay. title names the screen in the overlay.
type screenHelp struct {
	title   string
	columns []helpColumn
}

// footer renders the compact one-line legend from every binding flagged for it,
// as "key desc · key desc · …" — the shape the footers used before.
func (h screenHelp) footer() string {
	var parts []string
	for _, c := range h.columns {
		for _, k := range c.keys {
			if k.footer {
				parts = append(parts, k.key+" "+k.desc)
			}
		}
	}
	return strings.Join(parts, " · ")
}

// hasKeys reports whether the screen declared any bindings; ? is a no-op on a
// screen with nothing to show.
func (h screenHelp) hasKeys() bool {
	for _, c := range h.columns {
		if len(c.keys) > 0 {
			return true
		}
	}
	return false
}

// fuzzyMatch reports whether query occurs as a case-insensitive subsequence of
// target — the lazygit-style loose filter used by the overlay search box.
func fuzzyMatch(query, target string) bool {
	if query == "" {
		return true
	}
	q, t := strings.ToLower(query), strings.ToLower(target)
	qi := 0
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] == q[qi] {
			qi++
		}
	}
	return qi == len(q)
}

// helpModel is the ? overlay's state: whether it is open, the current filter
// text, and the vertical scroll offset into a long binding list.
type helpModel struct {
	active bool
	filter string
	offset int
}

// helpLayout is the overlay geometry and filtered body shared by key handling
// (which clamps the scroll offset) and rendering (which windows the body), so
// the two can never disagree about how tall the list is.
type helpLayout struct {
	innerW   int
	viewport int
	body     []string
}

// maxOffset is the furthest the body can scroll before its last row is visible.
func (l helpLayout) maxOffset() int {
	if m := len(l.body) - l.viewport; m > 0 {
		return m
	}
	return 0
}

// update advances the overlay for one key press and reports whether it should
// close. While open the overlay owns every key: esc/? close, arrows/pgup/pgdn
// scroll, backspace deletes, and any printable text narrows the filter.
func (hm helpModel) update(msg tea.KeyPressMsg, lay helpLayout) (helpModel, bool) {
	switch msg.String() {
	case "esc", "?", "ctrl+c":
		return hm, true
	case "up":
		hm.offset--
	case "down":
		hm.offset++
	case "pgup":
		hm.offset -= lay.viewport
	case "pgdown":
		hm.offset += lay.viewport
	case "home":
		hm.offset = 0
	case "end":
		hm.offset = lay.maxOffset()
	case "ctrl+u":
		hm.filter, hm.offset = "", 0
	case "backspace":
		if hm.filter != "" {
			hm.filter = hm.filter[:len(hm.filter)-1]
			hm.offset = 0
		}
	default:
		if msg.Mod == 0 && msg.Text != "" {
			hm.filter += msg.Text
			hm.offset = 0
		}
	}
	if hm.offset > lay.maxOffset() {
		hm.offset = lay.maxOffset()
	}
	if hm.offset < 0 {
		hm.offset = 0
	}
	return hm, false
}

// layoutHelp computes the overlay's width, visible row count, and full filtered
// body lines for a screen at the given terminal size and filter.
func layoutHelp(s Styles, h screenHelp, filter string, w, hgt int) helpLayout {
	innerW := w - 8
	if innerW > 60 {
		innerW = 60
	}
	if innerW < 20 {
		innerW = 20
	}

	// Widest key label across the rows that survive the filter, so descriptions
	// line up in a column (capped so one long chord can't eat the whole width).
	visible := make([][]helpKey, len(h.columns))
	keyW := 0
	for ci, c := range h.columns {
		for _, k := range c.keys {
			if fuzzyMatch(filter, k.key+" "+k.desc) {
				visible[ci] = append(visible[ci], k)
				if lw := lipgloss.Width(k.key); lw > keyW {
					keyW = lw
				}
			}
		}
	}
	if keyW > 12 {
		keyW = 12
	}
	descW := innerW - keyW - 4
	if descW < 4 {
		descW = 4
	}

	var body []string
	shown := 0
	for ci, c := range h.columns {
		ks := visible[ci]
		if len(ks) == 0 {
			continue
		}
		if shown > 0 {
			body = append(body, "")
		}
		shown++
		body = append(body, s.Subtle.Render(truncate(c.title, innerW)))
		for _, k := range ks {
			row := "  " + s.Header.Render(pad(truncate(k.key, keyW), keyW)) +
				"  " + s.Help.Render(truncate(k.desc, descW))
			body = append(body, row)
		}
	}
	if len(body) == 0 {
		body = []string{s.Help.Render("no matching keys")}
	}

	// Body viewport = box height budget minus the fixed chrome (title, search,
	// two blank spacers, footer legend = 5 rows).
	maxInnerH := hgt - 6
	if maxInnerH < 8 {
		maxInnerH = 8
	}
	viewport := maxInnerH - 5
	if viewport < 3 {
		viewport = 3
	}
	if viewport > len(body) {
		viewport = len(body)
	}
	return helpLayout{innerW: innerW, viewport: viewport, body: body}
}

// renderHelpPanel builds the floating overlay box: a title, a filter search box,
// the windowed binding list, and a nav legend, inside a brand-bordered card.
func renderHelpPanel(s Styles, h screenHelp, hm helpModel, w, hgt int) string {
	lay := layoutHelp(s, h, hm.filter, w, hgt)
	window, _, overflow := windowAt(lay.body, hm.offset, lay.viewport)
	for len(window) < lay.viewport {
		window = append(window, "")
	}

	title := "Help"
	if h.title != "" {
		title += " — " + h.title
	}

	search := s.Subtle.Render("› ")
	if hm.filter == "" {
		search += s.Help.Render("type to filter…")
	} else {
		search += s.Header.Render(truncate(hm.filter, lay.innerW-4)) + s.Info.Render("▌")
	}

	nav := "esc/? close"
	if overflow {
		nav = "↑↓ scroll · " + nav
	}

	lines := []string{s.Header.Render(truncate(title, lay.innerW)), search, ""}
	lines = append(lines, window...)
	lines = append(lines, "", s.Help.Render(nav))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Brand).
		Padding(0, 1).
		Width(lay.innerW).
		Render(strings.Join(lines, "\n"))
}

// compositeHelp floats the overlay panel over base, centered, using the
// lipgloss v2 compositor so the underlying screen is occluded but untouched.
func compositeHelp(s Styles, base string, h screenHelp, hm helpModel, w, hgt int) string {
	return centerOverlay(base, renderHelpPanel(s, h, hm, w, hgt), w, hgt)
}

// centerOverlay floats panel centered over base with the lipgloss v2 compositor,
// occluding but not mutating the base screen. Shared by the ? help overlay and the
// space peek layer so both float the same way.
func centerOverlay(base, panel string, w, hgt int) string {
	ox := (w - lipgloss.Width(panel)) / 2
	oy := (hgt - lipgloss.Height(panel)) / 2
	if ox < 0 {
		ox = 0
	}
	if oy < 0 {
		oy = 0
	}
	baseLayer := lipgloss.NewLayer(padToSize(base, w, hgt))
	overlay := lipgloss.NewLayer(panel).X(ox).Y(oy).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlay).Render()
}

// padToSize squares the base render to exactly w×h. lipgloss right-trims trailing
// whitespace, so a centered card measures narrower than the terminal; padding it
// back out keeps the composited frame the full terminal size (and fully occludes).
func padToSize(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if gap := w - lipgloss.Width(ln); gap > 0 {
			lines[i] = ln + strings.Repeat(" ", gap)
		}
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return strings.Join(lines, "\n")
}
