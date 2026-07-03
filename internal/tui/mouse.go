package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// Zone prefixes for the index-keyed lists and tabs each screen marks. Rows are
// marked as prefix+index; a click is resolved back with clickedRow.
const (
	zoneMenuRow    = "menu:"
	zoneMoreRow    = "more:"
	zoneStatusRow  = "status:" // renderQueue by ticket id
	zoneRecapRow   = "recap:"  // renderQueue by ticket id
	zoneHubRow     = "hub:"
	zoneSetRow     = "set:"
	zoneProvRow    = "prov:"
	zoneProvTab    = "provtab:"
	zoneLoopRow    = "loop:"
	zoneRunOnceRow = "run1:"
	zoneLogsRow    = "logrun:"
	zoneOnbTracker = "onbtrk:"
	zoneOnbProv    = "onbprv:"
	zoneOnbTeam    = "onbteam:"
	zoneOnbLabel   = "onblbl:"
	zoneOnbTimelog = "onbtl:"
	zoneOnbCI      = "onbci:"
)

// markRow wraps a rendered row in an index-keyed zone so a click can resolve which
// row was hit. An empty prefix leaves the row unmarked.
func markRow(prefix string, i int, row string) string {
	if prefix == "" {
		return row
	}
	return zone.Mark(prefix+strconv.Itoa(i), row)
}

// clickedRow reports the index of the marked row under the mouse, scanning the n
// rows a list drew, or false when the click missed every row.
func clickedRow(msg tea.MouseMsg, prefix string, n int) (int, bool) {
	for i := 0; i < n; i++ {
		if zone.Get(prefix + strconv.Itoa(i)).InBounds(msg) {
			return i, true
		}
	}
	return -1, false
}

// clickedQueueRow reports the index (into the given selectable rows) of the marked
// queue row under the mouse, for the renderQueue surfaces keyed by ticket id.
func clickedQueueRow(msg tea.MouseMsg, prefix string, rows []QueueRow) (int, bool) {
	for i, r := range rows {
		if zone.Get(prefix + r.ID).InBounds(msg) {
			return i, true
		}
	}
	return -1, false
}

// Zone ids for hit-testing. Rail rows are keyed by ticket id; footer verbs by
// their canonical (synthesizable) key so a click fires the same action the key
// would; whole panes are marked so the wheel can target the region under it.
const (
	zoneRailRow    = "rail:" // + ticket id
	zoneRail       = "pane:rail"
	zoneFooterVerb = "verb:" // + canonical key
)

// footerVerbKeys is the fixed universe of clickable footer keys, checked on each
// click. Only keys mapping to a single unambiguous action are here — movement
// legends (↑↓, ←→) are never marked, so they never appear.
var footerVerbKeys = []string{
	"enter", "esc", "space", "tab",
	"o", "l", "r", "b", "x", "R", "s", "a", "e", "v", "y", "f", "g", "q", "/",
}

// markVerbs wraps each "key desc" footer segment in a zone keyed by its canonical
// key, so clicking the verb fires the same action. Segments whose leading label
// isn't a single actionable key are left unmarked.
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

// verbKey extracts the canonical key of a "key desc" footer segment: the primary
// alternative of the leading label (esc/q → esc, enter/e → enter, ⇥ → tab). It
// returns false for movement legends that have no single click action.
func verbKey(part string) (string, bool) {
	fields := strings.Fields(part)
	if len(fields) == 0 {
		return "", false
	}
	tok := fields[0]
	if i := strings.IndexByte(tok, '/'); i > 0 {
		tok = tok[:i]
	}
	switch tok {
	case "enter", "esc", "space", "tab":
		return tok, true
	case "⇥":
		return "tab", true
	}
	if r := []rune(tok); len(r) == 1 && isActionRune(r[0]) {
		return tok, true
	}
	return "", false
}

func isActionRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '/'
}

// clickedFooterVerb reports the synthesized key for a footer verb under the mouse,
// or false when the click missed every marked verb.
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

// clickRailRow selects the rail row under the mouse; a click on the already-
// selected row activates it (attach for the live ticket, else peek), mirroring the
// keyboard's select-then-space/enter. It reports whether a row was hit.
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

// clickRecapRow selects the recap row under the mouse; a click on the already-
// selected row opens its PR, the recap's natural activate. Reports whether hit.
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

// handleMouseClick routes a left click on the dashboard: a footer verb fires its
// key, else a queue row selects/activates (the recap rows in the summary, the rail
// rows while running). Non-left buttons and misses fall through.
func (m model) handleMouseClick(msg tea.MouseClickMsg) (model, tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft {
		return m, nil, false
	}
	if k, ok := clickedFooterVerb(msg); ok {
		return m.handleKey(k)
	}
	if m.state == stateSummary {
		return m.clickRecapRow(msg)
	}
	return m.clickRailRow(msg)
}

// clickStatusRow selects the Status row under the mouse; a click on the already-
// selected row opens its PR (the natural activate). Rows are hit-tested against the
// same attention-sorted order renderQueue drew, which statusCursor indexes.
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

// handleMouseClick routes a left click through the app shell: a footer verb fires
// its key on the current screen; otherwise the click is resolved against the rows
// of the active screen (menus and Status inline, sub-model screens forwarded).
func (m appModel) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
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
	case viewRunOnce:
		m.runOnce, cmd = m.runOnce.Update(msg)
	case viewSettings:
		m.settings, cmd = m.settings.Update(msg)
	}
	return m, cmd
}

// This file is the mouse layer: the mouse-off toggle that hands drag-to-select
// back to the terminal, the OSC52 copy affordance, and the bubblezone hit-testing
// helpers the screens share. Mouse is a progressive enhancement — every action
// here has a keyboard equivalent, and turning the mouse off loses nothing.

// setMouseEnabled matches global zone hit-testing to the mouse mode, so a
// toggled-off mouse also stops the manager parsing markers it can no longer act on.
func setMouseEnabled(on bool) { zone.SetEnabled(on) }

// overlayMouseOff floats the mouse-off indicator over the bottom-right of the
// screen with the lipgloss compositor. Placing it here, over the finished frame,
// shows the mode on every screen without threading the flag through each footer.
func overlayMouseOff(s Styles, base string, w, h int) string {
	if w < 24 || h < 2 {
		return base
	}
	tag := s.Subtle.Render(" mouse off · ctrl+t to select ")
	x := w - lipgloss.Width(tag)
	if x < 0 {
		x = 0
	}
	baseLayer := lipgloss.NewLayer(padToSize(base, w, h))
	overlay := lipgloss.NewLayer(tag).X(x).Y(h - 1).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlay).Render()
}

// copyArtifact picks the most useful copyable value for a queue row and a label
// for the confirmation toast: the PR URL for a merged row, the preserved failure
// reason for a faulted one, the ticket ID otherwise. It mirrors peekContent's
// state ordering so a merged row's stale reason never wins over its PR.
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

// copySelectedArtifact copies the selected rail row's artifact to the system
// clipboard over OSC52 and sets the confirmation toast — the shared target of the
// y key on both dashboard paths and a rail row's copy click.
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
	return m
}
