package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestLogsCopyKeyYanksSelectedRunLog is the COD-709 contract: y in the log
// inspector copies the selected run's full log content over OSC52, shows a
// confirmation in the footer, and the next key dismisses it.
func TestLogsCopyKeyYanksSelectedRunLog(t *testing.T) {
	keyY := tea.KeyPressMsg{Code: 'y', Text: "y"}
	content := map[string]string{"COD-1": "══ COD-1 ══\nphase: merged\nbuild log body"}
	contentFn := func(id string) string { return content[id] }

	m := newLogsModel(DefaultStyles(), []LogRun{{ID: "COD-1"}}, 80, 24, contentFn)

	m, cmd := m.Update(keyY, contentFn)
	if cmd == nil {
		t.Fatal("y must return an OSC52 SetClipboard command")
	}
	if m.copied == "" || !strings.Contains(m.copied, "COD-1") {
		t.Fatalf("y must set a copy confirmation naming the run, got %q", m.copied)
	}
	if !strings.Contains(m.View(), "copied COD-1 log") {
		t.Fatal("the footer must show the copy confirmation")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}, contentFn)
	if m.copied != "" {
		t.Errorf("the next key must dismiss the confirmation, got %q", m.copied)
	}

	m.focused = true
	m, cmd = m.Update(keyY, contentFn)
	if cmd == nil {
		t.Fatal("y must copy from the viewport-focused pane too")
	}
}

// TestLogsCopyKeyNoRuns: with nothing to copy, y is a no-op.
func TestLogsCopyKeyNoRuns(t *testing.T) {
	m := newLogsModel(DefaultStyles(), nil, 80, 24, nil)
	m, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}, nil)
	if cmd != nil {
		t.Fatal("y with no runs must not emit a clipboard command")
	}
	if m.copied != "" {
		t.Fatalf("y with no runs must not claim a copy, got %q", m.copied)
	}
}
