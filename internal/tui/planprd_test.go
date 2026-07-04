package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderPRDMarkdownStyles pins the line-level markdown pass: headings drop
// their # markers, bullets become •, quotes a bar, rules a line, and fenced code
// passes through untouched.
func TestRenderPRDMarkdownStyles(t *testing.T) {
	s := DefaultStyles()
	md := strings.Join([]string{
		"# Widget export",
		"",
		"## Goals",
		"- fast",
		"* simple",
		"",
		"> a quote",
		"",
		"---",
		"",
		"```go",
		"# not a heading inside code",
		"```",
	}, "\n")

	got := ansi.Strip(renderPRDMarkdown(s, md, 80))

	for _, absent := range []string{"# Widget export", "## Goals", "- fast", "* simple", "> a quote", "---"} {
		if strings.Contains(got, absent) {
			t.Errorf("raw markup %q survived rendering", absent)
		}
	}
	for _, want := range []string{"Widget export", "Goals", "• fast", "• simple", "│ a quote", "─"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered PRD missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "# not a heading inside code") {
		t.Error("fenced code must pass through untouched")
	}
}

// TestRenderPRDTitleNotDoubled keeps the PRD title single: it is prepended as a
// heading only when the markdown does not already open with its own H1.
func TestRenderPRDTitleNotDoubled(t *testing.T) {
	s := DefaultStyles()
	doubled := renderPRD(s, "Widget export", "# Widget export\n\nbody", 80)
	if strings.Count(doubled, "Widget export") != 1 {
		t.Errorf("title doubled:\n%s", doubled)
	}
	titled := renderPRD(s, "Widget export", "body only", 80)
	if !strings.Contains(titled, "Widget export") {
		t.Errorf("title missing when the body has no H1:\n%s", titled)
	}
}
