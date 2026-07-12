package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/vterm"
)

const tailWindow = 6

// childSpan is a self-heal attempt nested under its phase. kind is the identity
// used to update a climbing counter in place (repair 1/3 → 2/3) instead of
// stacking a new row per attempt.
type childSpan struct {
	kind   string
	label  string
	detail string
}

func (c childSpan) text() string {
	if c.detail != "" {
		return c.label + " · " + c.detail
	}
	return c.label
}

var reAttempt = regexp.MustCompile(`(\d+)\s*/\s*(\d+)`)

// lastAttempt returns the final N/M pair in s — the current counter sits at the
// end of the log line, past any digits in an embedded error message.
func lastAttempt(s string) (n, m int, ok bool) {
	all := reAttempt.FindAllStringSubmatch(s, -1)
	if len(all) == 0 {
		return 0, 0, false
	}
	last := all[len(all)-1]
	n, _ = strconv.Atoi(last[1])
	m, _ = strconv.Atoi(last[2])
	return n, m, true
}

// parseChildSpan maps the pipeline's self-heal / retry / fallback log lines to a
// child span; ok is false for ordinary lines.
func parseChildSpan(line string) (childSpan, bool) {
	switch {
	case strings.Contains(line, "self-heal attempt "):
		if n, m, ok := lastAttempt(line); ok {
			return childSpan{kind: "repair", label: fmt.Sprintf("repair %d/%d", n, m)}, true
		}
	case strings.Contains(line, "comprehensive bugfix attempt "):
		if n, m, ok := lastAttempt(line); ok {
			return childSpan{kind: "bugfix", label: fmt.Sprintf("bugfix %d/%d", n, m)}, true
		}
	case strings.Contains(line, "pre-push gate"):
		if n, m, ok := lastAttempt(line); ok {
			return childSpan{kind: "repair", label: fmt.Sprintf("repair %d/%d", n, m), detail: "push"}, true
		}
	case strings.Contains(line, "falling back to "):
		prov := strings.TrimSpace(line[strings.Index(line, "falling back to ")+len("falling back to "):])
		return childSpan{kind: "fallback", label: "fallback", detail: prov}, true
	case strings.Contains(line, "retrying"):
		if n, m, ok := lastAttempt(line); ok {
			return childSpan{kind: "retry", label: fmt.Sprintf("retry %d/%d", n, m)}, true
		}
	}
	return childSpan{}, false
}

func upsertChildSpan(subs []childSpan, c childSpan) []childSpan {
	for i := range subs {
		if subs[i].kind == c.kind {
			subs[i] = c
			return subs
		}
	}
	return append(subs, c)
}

// spanDetail is a phase's trailing "elapsed  tag" fragment: live elapsed while
// active, the frozen duration once done, plus the model tag when known.
func spanDetail(st phaseStep) string {
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

// renderSpanList is the pipeline pane body: completed phases fold to one line, the
// active or failed phase expands with its child spans and a live tail, and the
// remaining pending phases collapse onto one compact row. width is the inner text
// width of the pane.
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
			pending = append(pending, m.styles.StepPending.Render("○ "+st.label))
		}
	}
	if len(pending) > 0 {
		rows = append(rows, ansi.Truncate(strings.Join(pending, "  "), width, "…"))
	}
	return strings.Join(rows, "\n")
}

func (m model) foldedSpan(st phaseStep, width int) string {
	line := m.styles.StepDone.Render("✓ " + st.label)
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
	head := style.Render(glyph + " " + m.steps[idx].label)
	if d := spanDetail(m.steps[idx]); d != "" {
		head += "  " + m.styles.StepTag.Render(d)
	}
	rows := []string{ansi.Truncate(head, width, "…")}
	for _, c := range m.steps[idx].subs {
		rows = append(rows, m.styles.StepTag.Render("  ↻ "+truncate(c.text(), width-4)))
	}
	for _, tl := range m.phaseTailLines(idx, tailWindow) {
		rows = append(rows, "  "+ansi.Truncate(tl, width-2, ""))
	}
	return rows
}

// phaseTailLines is the ~n-line live tail for a phase: the preserved snapshot once
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

// liveTail sources a phase's tail from the pty transcript it owns (agent phases)
// or, failing that, the feed lines tagged with the phase (PR/CI/merge).
func (m model) liveTail(idx, n int) []string {
	st := m.steps[idx]
	if st.transcript != "" && st.transcript == m.streamID && m.stream != nil {
		return vtermTail(m.stream, n)
	}
	return m.feedTail(st.label, n)
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
