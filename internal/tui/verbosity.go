package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type verbosityTier int

const (
	tierSpans verbosityTier = iota
	tierFeed
	tierRaw
)

func (t verbosityTier) next() verbosityTier { return (t + 1) % 3 }

func (t verbosityTier) short() string {
	switch t {
	case tierFeed:
		return "feed"
	case tierRaw:
		return "raw"
	default:
		return "spans"
	}
}

func (t verbosityTier) filterable() bool { return t == tierFeed || t == tierRaw }

func (m model) filterActive() bool { return m.filtering }

// cycleTier advances the tier and re-anchors to the tail with a clean filter, so
// each tier is entered as an unfiltered follow view.
func (m model) cycleTier() model {
	m.tier = m.tier.next()
	m.filtering = false
	m.filter = ""
	m.following = true
	m.refreshBody()
	return m
}

func (m model) tierContent(w int) string {
	switch m.tier {
	case tierFeed:
		return m.renderFeed(w)
	case tierRaw:
		return m.renderRaw(w)
	default:
		return m.renderSpanList(w)
	}
}

func (m model) spanPaneTitle() string {
	title := m.tierTitle()
	if lbl := m.filterLabel(); lbl != "" {
		title += "  " + lbl
	}
	return title
}

func (m model) tierTitle() string {
	switch m.tier {
	case tierFeed:
		return fmt.Sprintf("Activity feed · %d/%d", doneSteps(m.steps), len(m.steps))
	case tierRaw:
		return "Raw log"
	default:
		return fmt.Sprintf("Pipeline %d/%d", doneSteps(m.steps), len(m.steps))
	}
}

func (m model) filterLabel() string {
	if !m.tier.filterable() {
		return ""
	}
	switch {
	case m.filtering:
		return "/" + m.filter + "▌"
	case m.filter != "":
		return "/" + m.filter
	}
	return ""
}

func (m model) filterMatch(s string) bool {
	if m.filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(ansi.Strip(s)), strings.ToLower(m.filter))
}

// renderFeed lays out the classified activity feed. Continuation (↳) rows follow
// their parent's visibility so the filter never leaves a detail line orphaned.
func (m model) renderFeed(w int) string {
	if w < 12 {
		w = 12
	}
	var rows []string
	shownParent := false
	for i := range m.feed {
		e := m.feed[i]
		if e.sub {
			if shownParent {
				rows = append(rows, m.feedRow(e, w))
			}
			continue
		}
		shownParent = m.filterMatch(e.phase + " " + e.text)
		if shownParent {
			rows = append(rows, m.feedRow(e, w))
		}
	}
	if len(rows) == 0 {
		return m.emptyTier("no activity yet")
	}
	return strings.Join(rows, "\n")
}

func (m model) feedRow(e feedEntry, w int) string {
	if e.sub {
		row := m.styles.Help.Render("            ↳ ") + m.styles.Subtle.Render(e.text)
		return ansi.Truncate(row, w, "…")
	}
	head := m.styles.Help.Render(e.ts.Format("15:04:05")) + "  " +
		e.gstyle.Render(pad(e.glyph, 1)) + " " +
		m.styles.Help.Render(pad(e.phase, 8)) + " "
	return ansi.Truncate(head+e.text, w, "…")
}

// renderRaw shows the sanitized lines exactly as addLog received them, before
// classification collapsed them.
func (m model) renderRaw(w int) string {
	if w < 12 {
		w = 12
	}
	rows := make([]string, 0, len(m.raw))
	for _, ln := range m.raw {
		if m.filterMatch(ln) {
			rows = append(rows, ansi.Truncate(ln, w, "…"))
		}
	}
	if len(rows) == 0 {
		return m.emptyTier("no log lines yet")
	}
	return strings.Join(rows, "\n")
}

func (m model) emptyTier(msg string) string {
	if m.filter != "" {
		return m.styles.Subtle.Render("no rows match /" + m.filter)
	}
	return m.styles.Subtle.Render(msg)
}

// handleFilterKey drives the / filter input: text narrows live, enter applies and
// releases the input, esc clears it, scroll keys page through. ctrl+c is left to
// the shell's emergency stop.
func (m model) handleFilterKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		m.filtering = false
		m.filter = ""
		m.refreshBody()
		return m, nil, true
	case "enter":
		m.filtering = false
		m.refreshBody()
		return m, nil, true
	case "ctrl+u":
		m.filter = ""
		m.refreshBody()
		return m, nil, true
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.refreshBody()
		}
		return m, nil, true
	case "up", "down", "pgup", "pgdown", "home", "end":
		return m, nil, false
	default:
		if msg.Mod == 0 && msg.Text != "" {
			m.filter += msg.Text
			m.refreshBody()
		}
		return m, nil, true
	}
}
