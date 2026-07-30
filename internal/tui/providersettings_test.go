package tui

import (
	"slices"
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

// TestPhaseEditorNamesTheFixedDefault: the picker entry that clears an override
// says which model and effort the phase falls back to, and picking it deletes the
// per-phase keys rather than writing them empty.
func TestPhaseEditorNamesTheFixedDefault(t *testing.T) {
	acts := &fakeSettingsActions{
		tunings: []ProviderTuning{{
			Name:    "codex",
			Active:  true,
			Models:  []string{"gpt-5.6-sol", "gpt-5.4-mini"},
			Efforts: []string{"low", "medium"},
			Phases: []ProviderPhaseTuning{{
				Phase:     "build",
				DefModel:  "gpt-5.6-sol",
				DefEffort: "medium",
				EffModel:  "gpt-5.6-sol",
				EffEffort: "medium",
			}},
		}},
	}
	m := newProviderSettingsModel(acts, DefaultStyles(), 80, 40)
	m.cursor = len(m.rows) - 1
	m, _ = m.enterEdit()

	if got := m.edit.pickers[0].labels[0]; got != "(default: gpt-5.6-sol)" {
		t.Errorf("model sentinel = %q, want (default: gpt-5.6-sol)", got)
	}
	if got := m.edit.pickers[1].labels[0]; got != "(default: medium)" {
		t.Errorf("effort sentinel = %q, want (default: medium)", got)
	}
	_, cmd := m.saveEdit()
	if cmd == nil {
		t.Fatal("saving the phase editor produced no command")
	}
	cmd()

	want := []string{"CODEX_BUILD_MODEL", "CODEX_BUILD_EFFORT"}
	if got := acts.deletedKeys; !slices.Equal(got, want) {
		t.Errorf("sentinel deleted %v, want %v deleted (an empty value would mask a lower layer)", got, want)
	}
	if acts.saveCalled {
		t.Errorf("sentinel wrote %s=%q, want the key deleted instead", acts.savedKey, acts.savedValue)
	}
	for _, text := range []string{"gpt-5.6-sol", "medium"} {
		if !strings.Contains(m.edit.rowDesc, text) {
			t.Errorf("row description %q does not name the default %q", m.edit.rowDesc, text)
		}
	}
	if strings.Contains(m.View(), "inherit") {
		t.Errorf("phase editor still renders the inherit sentinel:\n%s", m.View())
	}
}
