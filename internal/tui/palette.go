package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// This file is the command palette: a global fuzzy launcher (ctrl+p everywhere,
// : outside text entry) floating over any screen. Its global actions are derived
// from the same menuItem registry the menu and More screens render, so there is
// no parallel action list to drift; typing a ticket id surfaces that ticket's
// state-appropriate verbs. It reuses the help overlay's compositing (padToSize,
// the lipgloss compositor) and the shared kit's row and scroll primitives.

// paletteCommand is one runnable entry in the palette. title is the text matched
// and shown; desc is a dim right-hand hint. run performs the command against the
// app model and returns the resulting model + cmd, mirroring selectAction — enter
// runs it and closes the palette.
type paletteCommand struct {
	title string
	desc  string
	run   func(appModel) (tea.Model, tea.Cmd)
}

// paletteModel is the palette's state: whether it is open, the current filter
// text, and the highlighted command.
type paletteModel struct {
	active bool
	filter string
	cursor int
}

// paletteLayout is the palette geometry and filtered command list shared by key
// handling (which clamps the cursor) and rendering (which windows the list), so
// the two can never disagree about how many commands there are.
type paletteLayout struct {
	innerW   int
	viewport int
	labelW   int
	commands []paletteCommand
}

func (l paletteLayout) count() int { return len(l.commands) }

// maxCursor is the last selectable index, or 0 when the list is empty.
func (l paletteLayout) maxCursor() int {
	if len(l.commands) == 0 {
		return 0
	}
	return len(l.commands) - 1
}

// update advances the palette for one key press. It returns the (possibly
// mutated) model, a chosen command when enter runs one, and whether the palette
// should close. While open the palette owns every key: esc/ctrl+p close, arrows
// move, backspace/ctrl+u edit the filter, and any printable text narrows it.
func (pm paletteModel) update(msg tea.KeyPressMsg, lay paletteLayout) (paletteModel, *paletteCommand, bool) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+p":
		return pm, nil, true
	case "enter":
		if pm.cursor >= 0 && pm.cursor < lay.count() {
			c := lay.commands[pm.cursor]
			return pm, &c, false
		}
		return pm, nil, false
	case "up", "ctrl+k":
		pm.cursor--
	case "down", "ctrl+j":
		pm.cursor++
	case "home":
		pm.cursor = 0
	case "end":
		pm.cursor = lay.maxCursor()
	case "ctrl+u":
		pm.filter, pm.cursor = "", 0
	case "backspace":
		if pm.filter != "" {
			pm.filter = pm.filter[:len(pm.filter)-1]
			pm.cursor = 0
		}
	default:
		if msg.Mod == 0 && msg.Text != "" {
			pm.filter += msg.Text
			pm.cursor = 0
		}
	}
	if pm.cursor > lay.maxCursor() {
		pm.cursor = lay.maxCursor()
	}
	if pm.cursor < 0 {
		pm.cursor = 0
	}
	return pm, nil, false
}

// layoutPalette computes the palette box width, visible row count, and label
// column width for the given commands at the terminal size.
func layoutPalette(s Styles, cmds []paletteCommand, w, hgt int) paletteLayout {
	innerW := w - 8
	if innerW > 64 {
		innerW = 64
	}
	if innerW < 24 {
		innerW = 24
	}

	labelW := 0
	for _, c := range cmds {
		if lw := lipgloss.Width(c.title); lw > labelW {
			labelW = lw
		}
	}
	if labelW > 32 {
		labelW = 32
	}

	n := len(cmds)
	if n < 1 {
		n = 1
	}
	// Body viewport = box height budget minus fixed chrome (title, search, two
	// blank spacers, nav legend = 5 rows).
	maxInnerH := hgt - 6
	if maxInnerH < 8 {
		maxInnerH = 8
	}
	viewport := maxInnerH - 5
	if viewport < 3 {
		viewport = 3
	}
	if viewport > n {
		viewport = n
	}
	return paletteLayout{innerW: innerW, viewport: viewport, labelW: labelW, commands: cmds}
}

// renderPalettePanel builds the floating palette box: a title, a filter search
// box, the windowed command list with the cursor highlighted, and a nav legend,
// inside a brand-bordered card.
func renderPalettePanel(s Styles, cmds []paletteCommand, pm paletteModel, w, hgt int) string {
	lay := layoutPalette(s, cmds, w, hgt)

	var rows []string
	for i, c := range lay.commands {
		focused := i == pm.cursor
		rows = append(rows, listRow(s, focused, truncate(c.title, lay.labelW), truncate(c.desc, lay.innerW-lay.labelW-4), lay.labelW))
	}
	if len(rows) == 0 {
		rows = []string{s.Help.Render("no matching commands")}
	}
	overflow := len(rows) > lay.viewport
	window := scrollToCursor(rows, pm.cursor, lay.viewport)
	for len(window) < lay.viewport {
		window = append(window, "")
	}

	search := s.Subtle.Render("› ")
	if pm.filter == "" {
		search += s.Help.Render("type a command or ticket id…")
	} else {
		search += s.Header.Render(truncate(pm.filter, lay.innerW-4)) + s.Info.Render("▌")
	}

	nav := "↑↓ select · enter run · esc close"
	if overflow {
		nav = "↑↓ scroll · " + nav
	}

	lines := []string{s.Header.Render(truncate("Command palette", lay.innerW)), search, ""}
	lines = append(lines, window...)
	lines = append(lines, "", s.Help.Render(nav))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Brand).
		Padding(0, 1).
		Width(lay.innerW).
		Render(strings.Join(lines, "\n"))
}

// compositePalette floats the palette panel over base, centered, using the
// lipgloss v2 compositor so the underlying screen is occluded but untouched.
func compositePalette(s Styles, base string, cmds []paletteCommand, pm paletteModel, w, hgt int) string {
	panel := renderPalettePanel(s, cmds, pm, w, hgt)
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

// paletteCommands is the palette's global action set, derived from the same
// menuItem registry the menu and More screens render — so a new menu action is
// automatically reachable from the palette with no parallel list to drift.
// Structural entries (More…, Back) are skipped; every real destination,
// including Quit, becomes a searchable command.
func (m appModel) paletteCommands() []paletteCommand {
	var cmds []paletteCommand
	seen := map[menuAction]bool{}
	add := func(items []menuItem) {
		for _, it := range items {
			switch it.action {
			case actMore, actBack:
				continue
			}
			if seen[it.action] {
				continue
			}
			seen[it.action] = true
			a := it.action
			cmds = append(cmds, paletteCommand{
				title: it.title,
				desc:  it.desc,
				run:   func(m appModel) (tea.Model, tea.Cmd) { return m.selectAction(a) },
			})
		}
	}
	add(m.items)
	add(m.moreItems)
	return cmds
}

// ticketCommands returns the verbs available for a well-formed ticket id, gated
// by what local state knows about it: any id can be run and opened in the
// tracker; a saved run adds "logs"; a live checkpoint adds "resume" and "reset".
func (m appModel) ticketCommands(id string) []paletteCommand {
	hasCheckpoint := false
	for _, r := range m.actions.StatusRows() {
		if r.ID == id {
			hasCheckpoint = true
			break
		}
	}
	hasLogs := false
	for _, r := range m.actions.LogRuns() {
		if r.ID == id {
			hasLogs = true
			break
		}
	}
	resumable := hasCheckpoint || m.info.Resume.ID == id

	var cmds []paletteCommand
	if resumable {
		cmds = append(cmds, paletteCommand{
			title: "Resume " + id,
			desc:  "continue from its checkpoint",
			run:   func(m appModel) (tea.Model, tea.Cmd) { return m.startRunTicket(id, "") },
		})
	} else {
		cmds = append(cmds, paletteCommand{
			title: "Run " + id,
			desc:  "run once through the pipeline",
			run:   func(m appModel) (tea.Model, tea.Cmd) { return m.startRunTicket(id, "") },
		})
	}
	if hasLogs {
		cmds = append(cmds, paletteCommand{
			title: "Logs " + id,
			desc:  "inspect its phase logs",
			run:   func(m appModel) (tea.Model, tea.Cmd) { return m.openLogsFor(id) },
		})
	}
	cmds = append(cmds, paletteCommand{
		title: "Open " + id,
		desc:  "view in tracker",
		run:   func(m appModel) (tea.Model, tea.Cmd) { return m, openURLCmd(linearIssueURL(id)) },
	})
	if hasCheckpoint {
		cmds = append(cmds, paletteCommand{
			title: "Reset " + id,
			desc:  "re-queue (confirm)",
			run:   func(m appModel) (tea.Model, tea.Cmd) { return m.resetForID(id) },
		})
	}
	return cmds
}

// paletteMatches is the filtered command list the palette renders: any ticket
// verbs for an id typed into the filter, then the global actions that fuzzily
// match the filter text.
func (m appModel) paletteMatches(filter string) []paletteCommand {
	var out []paletteCommand
	if id := extractTicketID(filter, m.info.Prefix); id != "" {
		out = append(out, m.ticketCommands(id)...)
	}
	for _, c := range m.paletteCommands() {
		if fuzzyMatch(filter, c.title+" "+c.desc) {
			out = append(out, c)
		}
	}
	return out
}

// paletteLayoutNow computes the palette layout for the current filter and size,
// shared by key handling and rendering.
func (m appModel) paletteLayoutNow() paletteLayout {
	return layoutPalette(m.styles, m.paletteMatches(m.palette.filter), m.width, m.height)
}

// openLogsFor opens the log inspector focused on id's run.
func (m appModel) openLogsFor(id string) (tea.Model, tea.Cmd) {
	m.subReturn = m.view
	m.logs = newLogsModel(m.styles, m.actions.LogRuns(), m.width, m.height, m.actions.LogContent).
		withFocus(id, m.actions.LogContent)
	m.view = viewLogs
	return m, nil
}

// resetForID lands on the reset screen with id pre-filled, so a single enter
// confirms the re-queue.
func (m appModel) resetForID(id string) (tea.Model, tea.Cmd) {
	m.subReturn = m.view
	m.reset.SetValue(id)
	m.reset.Placeholder = exampleID(m.info.Prefix)
	m.reset.Focus()
	m.result = ""
	m.busy = false
	m.view = viewReset
	return m, textinput.Blink
}
