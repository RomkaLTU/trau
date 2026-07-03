package tui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestProviderTuningBrowseScrolls is the AC3 regression for the browse view: a
// provider with many per-phase rows must not clip on a 24-row terminal, and the
// focused phase row must stay visible even though section headers sit between the
// cursor's logical index and its rendered line.
func TestProviderTuningBrowseScrolls(t *testing.T) {
	phases := make([]ProviderPhaseTuning, 15)
	for i := range phases {
		n := strconv.Itoa(i)
		phases[i] = ProviderPhaseTuning{Phase: "phase" + n, EffModel: "m" + n}
	}
	acts := &fakeSettingsActions{
		tunings: []ProviderTuning{{
			Name:   "claude",
			Active: true,
			Models: []string{"opus", "sonnet"},
			Model:  ProviderTuningField{Value: "opus", Layer: "project"},
			Phases: phases,
		}},
	}
	m := newProviderSettingsModel(acts, DefaultStyles(), 80, 24)

	// Cursor on the last phase row (rows = Model dial + 15 phases → index 15).
	m.cursor = len(m.rows) - 1
	view := m.View()
	if h := lipgloss.Height(view); h > 24 {
		t.Fatalf("browse view is %d rows on a 24-row terminal — content clipped", h)
	}
	if !strings.Contains(view, "phase14") {
		t.Errorf("focused last phase not visible after scrolling:\n%s", view)
	}
	// The Effective footer is fixed and stays visible however far the list scrolls.
	if !strings.Contains(view, "Effective:") {
		t.Errorf("Effective footer should stay pinned below the scrolled list:\n%s", view)
	}
}
