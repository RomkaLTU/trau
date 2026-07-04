package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// cardGeometry locates the centered card in a full-screen view by its rounded
// top-border corner, returning the card's left margin, width, and top row.
func cardGeometry(view string) (left, width, top int) {
	for row, ln := range strings.Split(view, "\n") {
		if strings.ContainsRune(ln, '╭') {
			trimmed := strings.TrimLeft(ln, " ")
			left = lipgloss.Width(ln) - lipgloss.Width(trimmed)
			width = lipgloss.Width(strings.TrimRight(trimmed, " "))
			return left, width, row
		}
	}
	return -1, -1, -1
}

// TestSettingsCardStableAcrossCursor is the COD-678 regression: moving the
// cursor between keys whose descriptions differ in length must not resize or
// reposition the centered card. Before the fix the card auto-sized to its
// widest line — the description — so a long description widened the card and
// lipgloss.Place re-centered it, shifting the whole container horizontally.
func TestSettingsCardStableAcrossCursor(t *testing.T) {
	items := []ConfigItem{
		{Key: "REMOTE", Value: "origin", Layer: "default", Description: "Git remote name"},
		{Key: "AGENT_TIMEOUT", Value: "3600", Layer: "default", Description: "Per-agent call hard timeout in seconds — a backstop for runaway calls; unproductive hangs are killed earlier by AGENT_STALL_WINDOW"},
		{Key: "NO_DESC", Value: "x", Layer: "user"},
		{Key: "MAX_ITERATIONS", Value: "15", Layer: "default", Description: "Maximum tickets per run"},
	}

	for _, dims := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		w, h := dims[0], dims[1]
		m := newSettingsModel(&fakeSettingsActions{items: items}, DefaultStyles(), w, h)

		m.cursor = 0
		wantLeft, wantWidth, wantTop := cardGeometry(m.View())
		if wantWidth < 0 {
			t.Fatalf("%dx%d: card border not found in view", w, h)
		}
		for i := 1; i < len(items); i++ {
			m.cursor = i
			left, width, top := cardGeometry(m.View())
			if left != wantLeft || width != wantWidth || top != wantTop {
				t.Errorf("%dx%d cursor=%d (%s): card moved to (left=%d,width=%d,top=%d), want (left=%d,width=%d,top=%d) — container flickers as the cursor moves",
					w, h, i, items[i].Key, left, width, top, wantLeft, wantWidth, wantTop)
			}
		}
	}
}
