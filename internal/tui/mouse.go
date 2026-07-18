package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// Index-keyed zone prefixes for the lists and tabs each screen marks (prefix+index).
const (
	zoneMenuRow    = "menu:"
	zoneMoreRow    = "more:"
	zoneStatusRow  = "status:" // renderQueue, by ticket id
	zoneRecapRow   = "recap:"  // renderQueue, by ticket id
	zoneHubRow     = "hub:"
	zoneSetRow     = "set:"
	zoneProvRow    = "prov:"
	zoneProvTab    = "provtab:"
	zoneLoopRow    = "loop:"
	zoneRunOnceRow = "run1:"
	zoneLogsRow    = "logrun:"
)

// Ticket-id-keyed rail rows, whole-pane marks for the wheel, and canonical-key
// footer verbs (prefix + key).
const (
	zoneRailRow    = "rail:"
	zoneRail       = "pane:rail"
	zoneFooterVerb = "verb:"
)

// markRow wraps a rendered row in an index-keyed zone; an empty prefix is a no-op.
func markRow(prefix string, i int, row string) string {
	if prefix == "" {
		return row
	}
	return zone.Mark(prefix+strconv.Itoa(i), row)
}

// clickedRow reports the index of the marked row under the mouse across n rows.
func clickedRow(msg tea.MouseMsg, prefix string, n int) (int, bool) {
	for i := 0; i < n; i++ {
		if zone.Get(prefix + strconv.Itoa(i)).InBounds(msg) {
			return i, true
		}
	}
	return -1, false
}

// clickedQueueRow reports the index (into rows) of the marked queue row under the mouse.
func clickedQueueRow(msg tea.MouseMsg, prefix string, rows []QueueRow) (int, bool) {
	for i, r := range rows {
		if zone.Get(prefix + r.ID).InBounds(msg) {
			return i, true
		}
	}
	return -1, false
}

// footerVerbKeys is the set of clickable footer keys; markVerbs and clickedFooterVerb
// share it so a marked verb is always handled. Movement legends (↑↓, ←→) are absent.
var footerVerbKeys = []string{
	"enter", "esc", "space", "tab",
	"o", "l", "r", "b", "x", "R", "s", "a", "e", "v", "y", "f", "g", "q", "/",
}

var footerVerbKeySet = func() map[string]bool {
	set := make(map[string]bool, len(footerVerbKeys))
	for _, k := range footerVerbKeys {
		set[k] = true
	}
	return set
}()

// markVerbs wraps each "key desc" footer segment in a zone keyed by its canonical key.
func markVerbs(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		if k, ok := verbKey(p); ok {
			out[i] = zone.Mark(zoneFooterVerb+k, p)
		} else {
			out[i] = p
		}
	}
	return out
}

// verbKey returns the canonical key of a "key desc" segment (esc/q → esc, ⇥ → tab),
// or false when it isn't a known clickable key.
func verbKey(part string) (string, bool) {
	fields := strings.Fields(part)
	if len(fields) == 0 {
		return "", false
	}
	tok := fields[0]
	if tok == "⇥" {
		tok = "tab"
	} else if i := strings.IndexByte(tok, '/'); i > 0 {
		tok = tok[:i]
	}
	if footerVerbKeySet[tok] {
		return tok, true
	}
	return "", false
}

// clickedFooterVerb returns the synthesized key for the footer verb under the mouse.
func clickedFooterVerb(msg tea.MouseMsg) (tea.KeyPressMsg, bool) {
	for _, k := range footerVerbKeys {
		if zone.Get(zoneFooterVerb + k).InBounds(msg) {
			return synthVerbKey(k), true
		}
	}
	return tea.KeyPressMsg{}, false
}

// synthVerbKey turns a canonical footer key back into the key press it stands for.
func synthVerbKey(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// clickRailRow selects the rail row under the mouse; a click on the selected row
// attaches the live ticket or peeks the rest. Reports whether a row was hit.
func (m model) clickRailRow(msg tea.MouseMsg) (model, tea.Cmd, bool) {
	if !m.railVisible() {
		return m, nil, false
	}
	active, _ := partitionQueue(m.queueRows(), m.foldDone())
	for i, r := range active {
		if !zone.Get(zoneRailRow + r.ID).InBounds(msg) {
			continue
		}
		wasSelected := i == m.queueCursor
		m.queueCursor = i
		if wasSelected {
			if attachTarget(r) {
				return m.attach()
			}
			m.peek = true
		}
		return m, nil, true
	}
	return m, nil, false
}

// clickRecapRow selects the recap row under the mouse; a click on the selected row
// opens its PR. Reports whether a row was hit.
func (m model) clickRecapRow(msg tea.MouseMsg) (model, tea.Cmd, bool) {
	active, _ := partitionQueue(m.queueRows(), m.foldDone())
	if i, ok := clickedQueueRow(msg, zoneRecapRow, active); ok {
		if i == m.queueCursor {
			return m, m.openSelectedPR(), true
		}
		m.queueCursor = i
		return m, nil, true
	}
	return m, nil, false
}

// handleMouseClick routes a left click on the dashboard: a footer verb, else a
// queue row. A modal (peek/filter) absorbs the click so nothing fires beneath it.
func (m model) handleMouseClick(msg tea.MouseClickMsg) (model, tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return m, nil, false
	}
	if m.peek || m.filtering {
		return m, nil, true
	}
	if k, ok := clickedFooterVerb(msg); ok {
		return m.handleKey(k)
	}
	if m.state == stateSummary {
		return m.clickRecapRow(msg)
	}
	return m.clickRailRow(msg)
}

// clickStatusRow selects the Status row under the mouse; a click on the selected row
// opens its PR. Rows are hit-tested in the same attention order statusCursor indexes.
func (m appModel) clickStatusRow(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	active, _ := partitionQueue(m.statusRows, false)
	if i, ok := clickedQueueRow(msg, zoneStatusRow, active); ok {
		if i == m.statusCursor {
			return m.handleStatusKey(synthVerbKey("o"))
		}
		m.statusCursor = i
	}
	return m, nil
}

// handleMouseClick routes a left click through the app shell: a footer verb, else
// the active screen's rows (menus and Status inline, sub-model screens forwarded).
// The global overlays absorb clicks while open.
func (m appModel) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if m.help.active || m.palette.active {
		return m, nil
	}
	if k, ok := clickedFooterVerb(msg); ok {
		return m.handleKey(k)
	}
	var cmd tea.Cmd
	switch m.view {
	case viewRunning:
		m.dash = m.dash.clearToast()
		m.dash, cmd = applyDashCmd(m.dash, msg)
	case viewMenu:
		if i, ok := clickedRow(msg, zoneMenuRow, len(m.items)); ok {
			if i == m.cursor {
				return m.selectAction(m.items[i].action)
			}
			m.cursor = i
		}
	case viewMore:
		if i, ok := clickedRow(msg, zoneMoreRow, len(m.moreItems)); ok {
			if i == m.moreCursor {
				return m.selectAction(m.moreItems[i].action)
			}
			m.moreCursor = i
		}
	case viewStatus:
		return m.clickStatusRow(msg)
	case viewLogs:
		m.logs, cmd = m.logs.Update(msg, m.actions.LogContent)
	case viewRunLoop:
		m.loopSetup, cmd = m.loopSetup.Update(msg)
		return m.afterLoopSetup(cmd)
	case viewRunOnce:
		m.runOnce, cmd = m.runOnce.Update(msg)
		return m.afterRunOnce(cmd)
	case viewSettings:
		m.settings, cmd = m.settings.Update(msg)
	case viewOnboarding:
		m.onboard, cmd = m.onboard.handleMouseClick(msg)
		if m.onboard.Done() {
			m = m.toMenu()
		}
	}
	return m, cmd
}

func setMouseEnabled(on bool) { zone.SetEnabled(on) }

// overlayMouseOff floats the mouse-off indicator over the bottom-right of the frame
// so the mode shows on every screen without threading the flag through each footer.
func overlayMouseOff(s Styles, base string, w, h int) string {
	tag := s.Subtle.Render(" mouse off — select/copy text · ctrl+t mouse on ")
	tw := lipgloss.Width(tag)
	if w < tw || h < 2 {
		return base
	}
	baseLayer := lipgloss.NewLayer(padToSize(base, w, h))
	overlay := lipgloss.NewLayer(tag).X(w - tw).Y(h - 1).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlay).Render()
}

// copyArtifact picks the copyable value and toast label for a queue row, ordered
// like peekContent so a merged row's stale reason never wins over its PR.
func copyArtifact(r QueueRow) (text, label string) {
	reason := oneLine(r.FailureReason)
	switch {
	case r.Phase == state.Merged && r.PRURL != "":
		return r.PRURL, "PR URL"
	case r.Phase != state.Merged && r.Phase != phaseReset && reason != "":
		return reason, "failure reason"
	case r.PRURL != "":
		return r.PRURL, "PR URL"
	default:
		return r.ID, "ticket ID"
	}
}

// copySelectedArtifact copies the selected row's artifact over OSC52 and sets the toast.
func (m model) copySelectedArtifact() (model, tea.Cmd) {
	sel, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	text, label := copyArtifact(sel)
	if text == "" {
		return m, nil
	}
	m.toast = "✓ copied " + label
	return m, tea.SetClipboard(text)
}

func (m model) clearToast() model {
	m.toast = ""
	m.toastErr = false
	return m
}
