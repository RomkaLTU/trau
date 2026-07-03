package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// verbosityTier is the run pane's detail level, cycled by v: the folded spans
// (default narrative), the full classified activity feed, or the raw
// pre-classification lines.
type verbosityTier int

const (
	tierSpans verbosityTier = iota
	tierFeed
	tierRaw
)

func (t verbosityTier) next() verbosityTier { return (t + 1) % 3 }

// short is the tier's one-word name for footer affordances (v <next>).
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

// filterable reports whether / narrows the tier — only the two log tiers, not
// the structured span view.
func (t verbosityTier) filterable() bool { return t == tierFeed || t == tierRaw }

// filterActive reports whether the filter input is capturing keys, so the app
// shell can route every key to the dash while a filter is being typed.
func (m model) filterActive() bool { return m.filtering }

// cycleTier advances the verbosity tier, drops any in-progress filter input, and
// re-anchors the pane to the tail of the newly selected content.
func (m model) cycleTier() model {
	m.tier = m.tier.next()
	m.filtering = false
	m.following = true
	m.refreshBody()
	return m
}

// tierContent renders the pane body for the active tier at inner width w.
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

// spanPaneTitle names the run pane by its active tier and, on a filterable tier,
// the live filter — so the tier and filter state are always visible.
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
		return "Activity feed"
	case tierRaw:
		return "Raw log"
	default:
		return fmt.Sprintf("Pipeline %d/%d", doneSteps(m.steps), len(m.steps))
	}
}

// filterLabel is the title's filter fragment: the query with a caret while typing,
// the query alone once applied, empty when unset or on the span tier.
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

// filterMatch reports whether s passes the active filter (case-insensitive
// substring). An empty filter matches everything.
func (m model) filterMatch(s string) bool {
	if m.filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(ansi.Strip(s)), strings.ToLower(m.filter))
}

// renderFeed lays out the full classified activity feed for a panel of inner text
// width w: a timestamp, glyph, phase column, and the classified text, with
// continuation lines hanging indented under their entry. The filter hides
// non-matching rows.
func (m model) renderFeed(w int) string {
	if w < 12 {
		w = 12
	}
	var rows []string
	for i := range m.feed {
		e := m.feed[i]
		if !m.filterMatch(e.phase + " " + e.text) {
			continue
		}
		rows = append(rows, m.feedRow(e, w))
	}
	if len(rows) == 0 {
		return m.emptyTier("no activity yet")
	}
	return strings.Join(rows, "\n")
}

func (m model) feedRow(e feedEntry, w int) string {
	if e.sub {
		indent := "            ↳ "
		return m.styles.Help.Render(indent) + m.styles.Subtle.Render(truncate(e.text, w-lipgloss.Width(indent)))
	}
	head := m.styles.Help.Render(e.ts.Format("15:04:05")) + "  " +
		e.gstyle.Render(pad(e.glyph, 1)) + " " +
		m.styles.Help.Render(pad(e.phase, 8)) + " "
	return head + truncate(e.text, w-lipgloss.Width(head))
}

// renderRaw shows the sanitized log lines exactly as addLog received them, before
// classification — the tier for debugging what the feed collapsed. The filter
// hides non-matching lines.
func (m model) renderRaw(w int) string {
	if w < 12 {
		w = 12
	}
	rows := make([]string, 0, len(m.raw))
	for _, ln := range m.raw {
		if !m.filterMatch(ln) {
			continue
		}
		rows = append(rows, ansi.Truncate(ln, w, ""))
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

// handleFilterKey drives the / filter input: text narrows the feed/raw tiers live,
// enter applies and releases the input, esc clears it. Scroll keys pass through so
// a filtered view can still be paged.
func (m model) handleFilterKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
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
