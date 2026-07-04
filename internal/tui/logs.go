package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// LogRun is one ticket run exposed by the log inspector.
type LogRun struct {
	ID            string
	Title         string
	Phase         string
	Updated       time.Time
	FailureReason string
}

// logsModel is the dedicated log-inspector view: a run list on the left and the
// selected run's phase logs on the right. Runs are ordered with the most recent
// update first; failed/quarantined runs are tinted so they stand out.
type logsModel struct {
	styles   Styles
	runs     []LogRun
	cursor   int
	viewport viewport.Model
	width    int
	height   int
	focused  bool // false = list focused, true = viewport focused
}

// newLogsModel creates a log inspector sized to the terminal. contentFn is
// called to load the selected run's log text.
func newLogsModel(styles Styles, runs []LogRun, width, height int, contentFn func(string) string) logsModel {
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Updated.After(runs[j].Updated)
	})
	m := logsModel{
		styles: styles,
		runs:   runs,
		width:  width,
		height: height,
	}
	m.viewport = viewport.New()
	m.viewport.SetContent("")
	if len(runs) > 0 && contentFn != nil {
		m.viewport.SetContent(contentFn(runs[0].ID))
	}
	m.relayout(width, height)
	return m
}

func (m *logsModel) relayout(width, height int) {
	m.width = width
	m.height = height
	if m.width < 20 {
		m.width = 20
	}
	if m.height < 8 {
		m.height = 8
	}

	bodyH := m.height - 4 // header(2) + hint(2)
	if bodyH < 4 {
		bodyH = 4
	}

	leftW := m.width / 3
	if leftW < 24 {
		leftW = 24
	}
	if leftW > 42 {
		leftW = 42
	}
	rightW := m.width - leftW - 1 // 1-char gap
	if rightW < 20 {
		rightW = 20
	}

	innerW := rightW - 4 // panel borders + padding
	innerH := bodyH - 2  // panel borders
	if innerW < 10 {
		innerW = 10
	}
	if innerH < 2 {
		innerH = 2
	}
	m.viewport.SetWidth(innerW)
	m.viewport.SetHeight(innerH)
}

// withFocus points the cursor at id's run (if present) and loads its log text,
// so the palette's per-ticket "logs" verb opens straight to that ticket.
func (m logsModel) withFocus(id string, contentFn func(string) string) logsModel {
	for i, r := range m.runs {
		if r.ID == id {
			m.cursor = i
			if contentFn != nil {
				m.viewport.SetContent(contentFn(id))
				m.viewport.GotoTop()
			}
			break
		}
	}
	return m
}

func (m logsModel) selected() (LogRun, bool) {
	if m.cursor < 0 || m.cursor >= len(m.runs) {
		return LogRun{}, false
	}
	return m.runs[m.cursor], true
}

func (m logsModel) isFailed(r LogRun) bool {
	return r.Phase == state.Quarantined || r.FailureReason != ""
}

// Update routes keys to the focused pane and resizes the viewport. The log
// viewport is always scrollable (f/b/u/d/pgup/pgdn/shift-arrows/g/G/mouse);
// arrow keys move the run list. tab swaps the visual focus indicator.
func (m logsModel) Update(msg tea.Msg, contentFn func(string) string) (logsModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.relayout(msg.Width, msg.Height)
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab":
			m.focused = !m.focused
			return m, nil
		case "up", "k":
			m.moveCursor(-1, contentFn)
			return m, nil
		case "down", "j":
			m.moveCursor(1, contentFn)
			return m, nil
		case "g":
			m.viewport.GotoTop()
			return m, nil
		case "G":
			m.viewport.GotoBottom()
			return m, nil
		case "shift+up":
			m.viewport.HalfPageUp()
			return m, nil
		case "shift+down":
			m.viewport.HalfPageDown()
			return m, nil
		}
		// All other keys (including the viewport's own f/b/u/d/pgup/pgdn)
		// go to the viewport so the log pane is always scrollable.
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			if i, ok := clickedRow(msg, zoneLogsRow, len(m.runs)); ok {
				m.moveCursor(i-m.cursor, contentFn)
				return m, nil
			}
		}
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	if m.focused {
		m.viewport, cmd = m.viewport.Update(msg)
	}
	return m, cmd
}

func (m *logsModel) moveCursor(delta int, contentFn func(string) string) {
	if len(m.runs) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.runs) {
		m.cursor = len(m.runs) - 1
	}
	if contentFn != nil {
		m.viewport.SetContent(contentFn(m.runs[m.cursor].ID))
		m.viewport.GotoTop()
	}
}

func (m logsModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}

	bodyH := m.height - 4
	if bodyH < 4 {
		bodyH = 4
	}
	leftW := m.width / 3
	if leftW < 24 {
		leftW = 24
	}
	if leftW > 42 {
		leftW = 42
	}
	rightW := m.width - leftW - 1
	if rightW < 20 {
		rightW = 20
		leftW = m.width - rightW - 1
		if leftW < 18 {
			leftW = 18
		}
	}

	leftPanel := titledPanel(m.styles, "runs", m.runListBody(leftW, bodyH), leftW, bodyH)
	rightPanel := titledPanel(m.styles, m.logTitle(), m.viewport.View(), rightW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	header := m.styles.Header.Render("⬡ trau") + "  " + m.styles.SummaryTitle.Render("logs")
	sep := m.styles.Separator.Render(strings.Repeat("─", m.width))
	hint := m.styles.Help.Render(m.help().footer())
	return header + "\n" + sep + "\n" + body + "\n" + sep + "\n" + hint
}

// help is the log inspector's key legend: the single source for its footer and
// the ? overlay. tab (switch pane) was hidden behind a static hint before.
func (m logsModel) help() screenHelp {
	return screenHelp{title: "Logs", columns: []helpColumn{
		group("Navigate",
			fk("↑↓", "pick"),
			xk("j/k", "pick"),
			fk("tab", "switch pane"),
		),
		group("Scroll log",
			fk("f/b/u/d", "scroll"),
			xk("shift+↑↓", "half-page"),
			fk("g/G", "jump"),
		),
		group("Session", fk("esc/q", "back"), xk("ctrl+t", "toggle mouse (select text)")),
	}}
}

func (m logsModel) logTitle() string {
	title := "log"
	if r, ok := m.selected(); ok {
		title = r.ID
	}
	if m.focused {
		title += " ●"
	}
	return title
}

func (m logsModel) runListBody(w, h int) string {
	innerW := w - 4
	innerH := h - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	if len(m.runs) == 0 {
		return m.styles.Subtle.Render("no saved runs")
	}

	// Scroll the list so the cursor stays visible.
	start := 0
	if m.cursor >= innerH {
		start = m.cursor - innerH + 1
	}
	end := start + innerH
	if end > len(m.runs) {
		end = len(m.runs)
	}

	lines := make([]string, 0, innerH)
	for i := start; i < end; i++ {
		lines = append(lines, markRow(zoneLogsRow, i, m.runLabel(m.runs[i], innerW, i == m.cursor)))
	}
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m logsModel) runLabel(r LogRun, w int, selected bool) string {
	phase := r.Phase
	if phase == "" {
		phase = "?"
	}
	updated := "—"
	if !r.Updated.IsZero() {
		updated = r.Updated.Format("01/02 15:04")
	}
	line := fmt.Sprintf("%s  %-12s %s", r.ID, phase, updated)

	fg := theme.Subtle
	switch {
	case m.isFailed(r):
		fg = theme.Error
	case state.Terminal(r.Phase):
		fg = theme.Success
	}

	style := lipgloss.NewStyle().Foreground(fg)
	if selected {
		style = lipgloss.NewStyle().Bold(true).Background(theme.Brand).Foreground(theme.Ink)
	}
	return style.Render(truncate(line, w))
}
