package tui

import (
	"strings"
)

// This file renders a PRD's markdown for the Plan screen's viewport: a light,
// line-level pass that styles headings, bullets, quotes, rules, and fenced code
// so the document reads as a document rather than raw markup. Prose wrapping is
// the viewport's job (soft wrap), so lines are never re-flowed here and the
// styling survives any resize.

// renderPRD builds the Plan viewport content: the PRD title as a heading over
// the styled markdown. A markdown body that opens with its own H1 is not
// double-titled.
func renderPRD(s Styles, title, markdown string, width int) string {
	body := renderPRDMarkdown(s, markdown, width)
	title = strings.TrimSpace(title)
	if title == "" || leadsWithH1(markdown) {
		return body
	}
	return s.SummaryTitle.Render(title) + "\n\n" + body
}

// leadsWithH1 reports whether the markdown's first non-blank line is a top-level
// heading — the document already carries its own title.
func leadsWithH1(markdown string) bool {
	for _, line := range strings.Split(markdown, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		return strings.HasPrefix(t, "# ")
	}
	return false
}

// renderPRDMarkdown styles markdown line by line. It is deliberately not a full
// renderer: headings drop their # markers and take the screen's heading styles,
// list bullets become a colored •, quotes a colored bar, thematic breaks a rule,
// and fenced code renders dimmed and untouched. Everything else passes through.
func renderPRDMarkdown(s Styles, markdown string, width int) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			out = append(out, s.Help.Render(line))
			continue
		}
		if inFence {
			out = append(out, s.Subtle.Render(line))
			continue
		}
		out = append(out, renderPRDLine(s, line, trimmed, width))
	}
	return strings.Join(out, "\n")
}

func renderPRDLine(s Styles, line, trimmed string, width int) string {
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	switch {
	case strings.HasPrefix(trimmed, "### "):
		return indent + s.Info.Render(strings.TrimPrefix(trimmed, "### "))
	case strings.HasPrefix(trimmed, "## "):
		return indent + s.StepActive.Render(strings.TrimPrefix(trimmed, "## "))
	case strings.HasPrefix(trimmed, "# "):
		return indent + s.SummaryTitle.Render(strings.TrimPrefix(trimmed, "# "))
	case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ "):
		return indent + s.Info.Render("•") + " " + trimmed[2:]
	case strings.HasPrefix(trimmed, "> "):
		return indent + s.Subtle.Render("│ "+strings.TrimPrefix(trimmed, "> "))
	case isThematicBreak(trimmed):
		return s.Separator.Render(strings.Repeat("─", ruleWidth(width)))
	default:
		return line
	}
}

// isThematicBreak reports a ---/***/___ horizontal rule line.
func isThematicBreak(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	for _, set := range []rune{'-', '*', '_'} {
		if trimmed == strings.Repeat(string(set), len(trimmed)) {
			return true
		}
	}
	return false
}

func ruleWidth(width int) int {
	if width > 40 {
		return 40
	}
	if width < 3 {
		return 3
	}
	return width
}
