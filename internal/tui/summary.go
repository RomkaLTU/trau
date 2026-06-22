package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/state"
)

// statusKind buckets a ticket's terminal phase for coloring.
type statusKind int

const (
	statusOK   statusKind = iota // merged
	statusWarn                   // PR open, awaiting human merge
	statusBad                    // quarantined / incomplete
)

// statusLabel maps a final state checkpoint to a human label + kind.
func statusLabel(phase string) (string, statusKind) {
	switch phase {
	case state.Merged:
		return "merged", statusOK
	case state.PROpen:
		return "PR open", statusWarn
	case state.Quarantined:
		return "quarantined", statusBad
	default:
		return "incomplete", statusBad
	}
}

func statusGlyph(k statusKind) string {
	switch k {
	case statusOK:
		return "✓"
	case statusWarn:
		return "◔"
	default:
		return "⚠"
	}
}

// enterSummary flips the model to the completion screen. It deliberately does
// NOT quit — the program stays up showing the recap until the user dismisses it
// (the old behavior quit in the same frame, so the summary never appeared).
func (m model) enterSummary(s console.SessionSummary) (tea.Model, tea.Cmd) {
	m.state = stateSummary
	m.summary = s
	m.banner = ""
	m.summaryTable = m.makeSummaryTable()
	return m, nil
}

// makeSummaryTable builds the per-ticket results table sized to the window.
func (m model) makeSummaryTable() table.Model {
	idW, resW, timeW, costW := 8, 14, 7, 9

	titleW := m.width - (idW + resW + timeW + costW) - 18
	if titleW < 12 {
		titleW = 12
	}
	cols := []table.Column{
		{Title: "ID", Width: idW},
		{Title: "Title", Width: titleW},
		{Title: "Result", Width: resW},
		{Title: "Time", Width: timeW},
		{Title: "Cost", Width: costW},
	}
	rows := make([]table.Row, 0, len(m.results))
	for _, r := range m.results {
		label, kind := statusLabel(r.Phase)
		rows = append(rows, table.Row{
			r.ID,
			truncate(firstNonEmpty(r.Title, "—"), titleW),
			statusGlyph(kind) + " " + label,
			fmtDur(r.Elapsed),
			"$" + strconv.FormatFloat(r.Cost, 'f', 2, 64),
		})
	}
	h := len(rows) + 1
	if h > 14 {
		h = 14
	}
	if h < 2 {
		h = 2
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(h),
	)
	st := table.DefaultStyles()
	st.Header = st.Header.Bold(true).Foreground(colorSubtle).
		BorderBottom(true).BorderForeground(colorFaint)
	st.Selected = st.Selected.Foreground(lipgloss.Color("#FFFFFF")).
		Background(colorBrand).Bold(false)
	t.SetStyles(st)
	return t
}

// renderSummary draws the centered completion card with totals + the table.
func (m model) renderSummary() string {
	title := m.styles.SummaryTitle.Render("trau · session complete")

	var head string
	switch {
	case m.summary.Paused:
		reason := "provider rate/usage limit reached"
		if m.summary.Err != nil {
			reason = m.summary.Err.Error()
		}
		head = m.styles.Warning.Render("⏸ " + reason + " — work saved; rerun trau to resume")
	case m.summary.Err != nil:
		head = m.styles.Error.Render("aborted: " + m.summary.Err.Error())
	default:
		head = m.styles.Subtle.Render(m.totalsLine())
	}

	body := title + "\n" + head
	if len(m.results) > 0 {
		body += "\n\n" + m.summaryTable.View()
	}
	card := m.styles.SummaryCard.MaxWidth(m.width).Render(body)
	hint := m.styles.Help.Render("↑↓ move · o open PR · enter/q exit")

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, card, hint))
}

// totalsLine summarizes the session: counts by outcome, elapsed, cost, tokens.
func (m model) totalsLine() string {
	merged, quarantined := 0, 0
	for i := range m.results {
		switch _, k := statusLabel(m.results[i].Phase); k {
		case statusOK:
			merged++
		case statusBad:
			quarantined++
		}
	}
	noun := "tickets"
	if m.summary.Tickets == 1 {
		noun = "ticket"
	}
	parts := []string{fmt.Sprintf("%d %s", m.summary.Tickets, noun)}
	if merged > 0 {
		parts = append(parts, m.styles.Success.Render(fmt.Sprintf("%d merged", merged)))
	}
	if quarantined > 0 {
		parts = append(parts, m.styles.Error.Render(fmt.Sprintf("%d quarantined", quarantined)))
	}
	parts = append(parts, fmtDur(m.summary.Elapsed))
	parts = append(parts, fmt.Sprintf("~$%s est", strconv.FormatFloat(m.summary.TotalCost, 'f', 2, 64)))
	if m.summary.TotalTokens > 0 {
		parts = append(parts, fmtTokens(m.summary.TotalTokens)+" tokens")
	}
	return strings.Join(parts, " · ")
}

// openSelectedPR opens the focused ticket's PR in the browser, if it has one.
func (m model) openSelectedPR() tea.Cmd {
	idx := m.summaryTable.Cursor()
	if idx < 0 || idx >= len(m.results) {
		return nil
	}
	url := m.results[idx].PRURL
	if url == "" {
		return nil
	}
	return func() tea.Msg {
		_ = openURL(url)
		return nil
	}
}

// openURL launches the OS browser/handler for url. Best-effort: errors are
// swallowed (the TUI shouldn't crash because a browser is missing).
func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// fmtTokens renders a token count compactly: 705 → "705", 11137 → "11.1k".
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

func firstNonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
