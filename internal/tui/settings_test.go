package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type fakeSettingsActions struct {
	items      []ConfigItem
	tunings    []ProviderTuning
	saveCalled bool
	savedKey   string
	savedValue string
	savedLayer string
}

func (f *fakeSettingsActions) ConfigItems() []ConfigItem { return f.items }

func (f *fakeSettingsActions) SaveConfigItem(key, value, layer string) error {
	f.saveCalled = true
	f.savedKey = key
	f.savedValue = value
	f.savedLayer = layer
	return nil
}

func (f *fakeSettingsActions) ConfigLayers() []string { return []string{"local", "project", "user"} }

func (f *fakeSettingsActions) ProviderTunings() []ProviderTuning { return f.tunings }

// TestSettingsListScrollsToCursor is the AC1 regression: a 60-key list on a
// 24-row terminal must not clip — the view stays within the terminal, and the
// focused key's row and description stay visible however far the cursor scrolls.
func TestSettingsListScrollsToCursor(t *testing.T) {
	items := make([]ConfigItem, 60)
	for i := range items {
		n := strconv.Itoa(i)
		items[i] = ConfigItem{
			Key:         "KEY_" + n,
			Value:       "v" + n,
			Layer:       "project",
			Description: "describes key number " + n,
		}
	}
	m := newSettingsModel(&fakeSettingsActions{items: items}, DefaultStyles(), 80, 24)

	// Cursor at the bottom: the last key and its description must both render,
	// and nothing may spill past the 24-row terminal.
	m.cursor = len(items) - 1
	view := m.View()
	if h := lipgloss.Height(view); h > 24 {
		t.Fatalf("view is %d rows on a 24-row terminal — content clipped", h)
	}
	if !strings.Contains(view, "KEY_59") {
		t.Errorf("focused key KEY_59 not visible after scrolling:\n%s", view)
	}
	if !strings.Contains(view, "describes key number 59") {
		t.Errorf("focused key's description not visible after scrolling:\n%s", view)
	}
	// A key from the top of the list has scrolled out of view.
	if strings.Contains(view, "KEY_0 ") {
		t.Errorf("top-of-list key should have scrolled off, but KEY_0 is still shown:\n%s", view)
	}

	// Cursor at the top: the first key is back in view.
	m.cursor = 0
	if top := m.View(); !strings.Contains(top, "KEY_0") {
		t.Errorf("first key not visible with cursor at top:\n%s", top)
	}
}

func TestSettingsFiltersAdvancedByDefault(t *testing.T) {
	acts := &fakeSettingsActions{
		items: []ConfigItem{
			{Key: "BASE_BRANCH", Value: "main", Layer: "project", Advanced: false},
			{Key: "CLAUDE_FLAGS", Value: "--foo", Layer: "user", Advanced: true},
		},
	}
	m := newSettingsModel(acts, DefaultStyles(), 80, 24)
	if len(m.filtered) != 1 || m.filtered[0].Key != "BASE_BRANCH" {
		t.Fatalf("expected only BASE_BRANCH, got %+v", m.filtered)
	}
}

func TestSettingsToggleAdvanced(t *testing.T) {
	acts := &fakeSettingsActions{
		items: []ConfigItem{
			{Key: "BASE_BRANCH", Value: "main", Layer: "project", Advanced: false},
			{Key: "CLAUDE_FLAGS", Value: "--foo", Layer: "user", Advanced: true},
		},
	}
	m := newSettingsModel(acts, DefaultStyles(), 80, 24)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 items with advanced shown, got %d", len(m.filtered))
	}
}

func TestSettingsEnterEdit(t *testing.T) {
	acts := &fakeSettingsActions{
		items: []ConfigItem{
			{Key: "BASE_BRANCH", Value: "main", Layer: "project", Advanced: false},
		},
	}
	m := newSettingsModel(acts, DefaultStyles(), 80, 24)
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected blink command on enter edit")
	}
	if m.step != settingsEdit {
		t.Fatalf("expected step edit, got %d", m.step)
	}
	if m.editKey != "BASE_BRANCH" {
		t.Fatalf("expected editKey BASE_BRANCH, got %s", m.editKey)
	}
	if m.editInput.Value() != "main" {
		t.Fatalf("expected input value main, got %s", m.editInput.Value())
	}
}

func TestSettingsEditCancel(t *testing.T) {
	acts := &fakeSettingsActions{
		items: []ConfigItem{
			{Key: "BASE_BRANCH", Value: "main", Layer: "project", Advanced: false},
		},
	}
	m := newSettingsModel(acts, DefaultStyles(), 80, 24)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.step != settingsList {
		t.Fatalf("expected step list after cancel, got %d", m.step)
	}
	if acts.saveCalled {
		t.Fatal("save should not be called on cancel")
	}
}

func TestSettingsSave(t *testing.T) {
	acts := &fakeSettingsActions{
		items: []ConfigItem{
			{Key: "BASE_BRANCH", Value: "main", Layer: "project", Advanced: false},
		},
	}
	m := newSettingsModel(acts, DefaultStyles(), 80, 24)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.editInput.SetValue("develop")
	m.editLayer = 1 // project
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.step != settingsSaving {
		t.Fatalf("expected step saving, got %d", m.step)
	}
	if cmd == nil {
		t.Fatal("expected save command")
	}

	msg := cmd()
	done, ok := msg.(saveConfigDoneMsg)
	if !ok {
		t.Fatalf("expected saveConfigDoneMsg, got %T", msg)
	}
	if done.err != nil {
		t.Fatal(done.err)
	}
	if done.key != "BASE_BRANCH" || done.value != "develop" || done.layer != "project" {
		t.Fatalf("unexpected save msg: %+v", done)
	}
}

func TestSettingsInList(t *testing.T) {
	acts := &fakeSettingsActions{
		items: []ConfigItem{
			{Key: "BASE_BRANCH", Value: "main", Layer: "project", Advanced: false},
		},
	}
	m := newSettingsModel(acts, DefaultStyles(), 80, 24)
	if !m.InList() {
		t.Fatal("expected InList true")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.InList() {
		t.Fatal("expected InList false while editing")
	}
}
