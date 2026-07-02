package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQASpaceTypedIntoOnboardingAPIKeyField(t *testing.T) {
	m := newOnboardingModel(context.Background(), &fakeOnboardActions{}, DefaultStyles(), 80, 24)
	m.step = onboardLinearAPIKey
	m.apiKeyInputFocused = true
	m.apiKey.Focus()

	m = typeRunes(m, "lin_api")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = typeRunes(m, "x-1._Z")

	if got := m.apiKey.Value(); got != "lin_api x-1._Z" {
		t.Fatalf("api key value = %q, space or punctuation was eaten", got)
	}
	if m.step != onboardLinearAPIKey {
		t.Fatalf("space navigated away from the API key step: step=%d", m.step)
	}
}

func TestQASpaceTypedIntoJiraFields(t *testing.T) {
	m := newOnboardingModel(context.Background(), &fakeOnboardActions{}, DefaultStyles(), 80, 24)
	m.step = onboardJiraCreds
	m.jiraFieldCursor = 0
	m.focusJiraField()

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if got := m.jiraBaseURL.Value(); got != " " {
		t.Fatalf("jira base URL value = %q, space was eaten", got)
	}
	if m.jiraFieldCursor != 0 {
		t.Fatalf("space moved the jira field cursor to %d", m.jiraFieldCursor)
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
	m := newOnboardingModel(context.Background(), &fakeOnboardActions{}, DefaultStyles(), 80, 24)
	m.step = onboardBaseBranch
	m.baseBranchInputFocused = true
	m.baseBranch.Focus()
	before := m.branchingCursor

	m = typeRunes(m, "jk")
	if got := m.baseBranch.Value(); got != "jk" {
		t.Fatalf("base branch value = %q, j/k were treated as navigation", got)
	}
	if m.branchingCursor != before {
		t.Fatal("typing j/k into the focused base-branch field moved the branching cursor")
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
