package tui

import (
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/vterm"
)

const tailWindow = 6

var reActivityDigit = regexp.MustCompile(`([a-zA-Z])([0-9])`)

// activityText renders an Activity with its optional raw detail as a compact label:
// "repair2" becomes "repair 2", a bare Activity stays as it is. It mirrors the web
// stepper (web/src/lib/steps.ts) so the two surfaces read identically.
func activityText(act activity.Activity, detail string) string {
	base := strings.TrimSpace(detail)
	if base == "" {
		base = string(act)
	}
	return reActivityDigit.ReplaceAllString(base, "$1 $2")
}

// subLabel is the active Step's live Activity line, e.g. "Verify · repair 2".
func (st stepRow) subLabel() string {
	if st.act == "" {
		return ""
	}
	return string(st.step) + " · " + activityText(st.act, st.detail)
}

// spanDetail is a Step's trailing "elapsed  tag" fragment: live elapsed while
// active, the frozen duration once done, plus the model tag when known.
func spanDetail(st stepRow) string {
	var parts []string
	switch {
	case st.state == stepActive && !st.start.IsZero():
		parts = append(parts, fmtDur(time.Since(st.start)))
	case st.took > 0:
		parts = append(parts, fmtDur(st.took))
	}
	if st.tag != "" {
		parts = append(parts, st.tag)
	}
	return strings.Join(parts, "  ")
}

// renderSpanList is the pipeline pane body: completed Steps fold to one line, the
// active or failed Step expands with its live sub-label and tail, and the remaining
// pending Steps collapse onto one compact row. width is the inner text width.
func (m model) renderSpanList(width int) string {
	if width < 12 {
		width = 12
	}
	var rows []string
	var pending []string
	for i := range m.steps {
		st := m.steps[i]
		switch st.state {
		case stepDone:
			rows = append(rows, m.foldedSpan(st, width))
		case stepActive:
			rows = append(rows, m.expandedSpan(i, width, false)...)
		case stepFailed:
			rows = append(rows, m.expandedSpan(i, width, true)...)
		default:
			pending = append(pending, m.styles.StepPending.Render("○ "+string(st.step)))
		}
	}
	if len(pending) > 0 {
		rows = append(rows, ansi.Truncate(strings.Join(pending, "  "), width, "…"))
	}
	return strings.Join(rows, "\n")
}

func (m model) foldedSpan(st stepRow, width int) string {
	line := m.styles.StepDone.Render("✓ " + string(st.step))
	if d := spanDetail(st); d != "" {
		line += "  " + m.styles.StepTag.Render(d)
	}
	return ansi.Truncate(line, width, "…")
}

func (m model) expandedSpan(idx, width int, failed bool) []string {
	style, glyph := m.styles.StepActive, strings.TrimSpace(ansi.Strip(m.spin.View()))
	if failed {
		style, glyph = m.styles.StepFailed, "✗"
	}
	st := m.steps[idx]
	head := style.Render(glyph + " " + string(st.step))
	if d := spanDetail(st); d != "" {
		head += "  " + m.styles.StepTag.Render(d)
	}
	rows := []string{ansi.Truncate(head, width, "…")}
	if !failed {
		if sub := st.subLabel(); sub != "" {
			rows = append(rows, m.styles.StepTag.Render("  "+truncate(sub, width-2)))
		}
	}
	for _, tl := range m.phaseTailLines(idx, tailWindow) {
		rows = append(rows, "  "+ansi.Truncate(tl, width-2, ""))
	}
	return rows
}

// phaseTailLines is the ~n-line live tail for a Step: the preserved snapshot once
// it has failed, else the current live output.
func (m model) phaseTailLines(idx, n int) []string {
	if idx < 0 || idx >= len(m.steps) {
		return nil
	}
	if snap := m.steps[idx].tailSnapshot; len(snap) > 0 {
		return lastN(snap, n)
	}
	return m.liveTail(idx, n)
}

// liveTail sources a Step's tail from the pty transcript it owns (agent phases) or,
// failing that, the feed lines tagged with the Step (commit/PR/CI/merge).
func (m model) liveTail(idx, n int) []string {
	st := m.steps[idx]
	if st.transcript != "" && st.transcript == m.streamID && m.stream != nil {
		return vtermTail(m.stream, n)
	}
	return m.feedTail(string(st.step), n)
}

func vtermTail(s *vterm.Screen, n int) []string {
	lines := s.Lines()
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lastN(lines, n)
}

func (m model) feedTail(phase string, n int) []string {
	lines := make([]string, 0, n)
	for i := len(m.feed) - 1; i >= 0 && len(lines) < n; i-- {
		e := m.feed[i]
		if e.sub || e.glyph == "▸" || e.phase != phase {
			continue
		}
		lines = append(lines, e.gstyle.Render(pad(e.glyph, 1))+" "+e.text)
	}
	for l, r := 0, len(lines)-1; l < r; l, r = l+1, r-1 {
		lines[l], lines[r] = lines[r], lines[l]
	}
	return lines
}

func lastN(s []string, n int) []string {
	if n < 0 {
		n = 0
	}
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
