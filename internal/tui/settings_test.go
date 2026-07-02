package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
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
