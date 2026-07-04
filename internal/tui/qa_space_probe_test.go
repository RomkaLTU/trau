package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQASpaceTypedIntoOnboardingAPIKeyField(t *testing.T) {
	m := formModel(&fakeOnboardActions{repoRoot: t.TempDir()}, "linear")
	m = pressKey(m, tea.KeyEnter) // tracker → ai provider
	m = pressKey(m, tea.KeyEnter) // → linear key
	if m.focusedKey() != keyLinearKey {
		t.Fatalf("focused key = %q, want %q", m.focusedKey(), keyLinearKey)
	}

	m = typeRunes(m, "lin_api")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = typeRunes(m, "x-1._Z")

	if got := m.fv.linearKey; got != "lin_api x-1._Z" {
		t.Fatalf("api key value = %q, space or punctuation was eaten", got)
	}
	if m.focusedKey() != keyLinearKey {
		t.Fatalf("space navigated away from the API key step: %q", m.focusedKey())
	}
}

func TestQASpaceTypedIntoJiraFields(t *testing.T) {
	m := formModel(&fakeOnboardActions{repoRoot: t.TempDir()}, "jira")
	m = pressKey(m, tea.KeyEnter)
	m = pressKey(m, tea.KeyEnter) // → jira base URL
	if m.focusedKey() != keyJiraBase {
		t.Fatalf("focused key = %q, want %q", m.focusedKey(), keyJiraBase)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if got := m.fv.jiraBase; got != " " {
		t.Fatalf("jira base URL value = %q, space was eaten", got)
	}
	if m.focusedKey() != keyJiraBase {
		t.Fatalf("space moved off the jira base field: %q", m.focusedKey())
	}
}

func TestQASpaceTypedIntoRunOnceInput(t *testing.T) {
	m := newRunOnceModel(context.Background(), nil, DefaultStyles(), MenuInfo{Prefix: "COD"}, 80, 24)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'C', Text: "C"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if got := m.input.Value(); got != "C 1" {
		t.Fatalf("run-once input value = %q, space was eaten", got)
	}
	if m.step != runOnceConfirm {
		t.Fatalf("space changed the run-once step to %d", m.step)
	}
}

func TestQASpaceTogglesSettingsBool(t *testing.T) {
	acts := &fakeSettingsActions{
		items: []ConfigItem{
			{Key: "AUTO_MERGE", Value: "1", Bool: true, Layer: "default"},
		},
	}
	m := newSettingsModel(acts, DefaultStyles(), 80, 24)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.editKind != editBool {
		t.Fatalf("expected bool editor, got kind %d", m.editKind)
	}
	before := m.editOptIdx
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if m.editOptIdx == before {
		t.Fatal("space did not toggle the boolean value")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if m.editOptIdx != before {
		t.Fatal("second space did not toggle the boolean back")
	}
}

func TestQATypingJIntoTextFieldDoesNotNavigate(t *testing.T) {
	m := formModel(&fakeOnboardActions{repoRoot: t.TempDir()}, "github")
	m = pressKey(m, tea.KeyEnter)
	m = pressKey(m, tea.KeyEnter) // → base branch (linear + jira groups hidden for github)
	if m.focusedKey() != keyBaseBranch {
		t.Fatalf("focused key = %q, want %q", m.focusedKey(), keyBaseBranch)
	}

	m = typeRunes(m, "jk")
	if got := m.fv.baseBranch; got != "jk" {
		t.Fatalf("base branch value = %q, j/k were treated as navigation", got)
	}
	if m.focusedKey() != keyBaseBranch {
		t.Fatal("typing j/k moved off the base-branch field")
	}
}

func TestQADashboardCtrlCStopsThenForceQuits(t *testing.T) {
	interrupted := false
	m := initialModel(func() { interrupted = true })
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	m, cmd, handled := m.handleKey(ctrlC)
	if !handled || !m.stopping || !interrupted {
		t.Fatalf("first ctrl+c: handled=%v stopping=%v interrupted=%v", handled, m.stopping, interrupted)
	}
	if cmd != nil {
		t.Fatal("first ctrl+c must not quit the program")
	}
	if m.banner == "" {
		t.Fatal("first ctrl+c did not show a stopping banner")
	}

	_, cmd, handled = m.handleKey(ctrlC)
	if !handled || cmd == nil {
		t.Fatal("second ctrl+c did not force-quit")
	}
}
