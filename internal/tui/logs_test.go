package tui

import (
	"fmt"
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

// TestLogsCopyPathYanksLogPath: Y copies the selected run's on-disk log path —
// distinct from y, which copies the log body — names it in the confirmation, and
// the next key dismisses it.
func TestLogsCopyPathYanksLogPath(t *testing.T) {
	keyShiftY := tea.KeyPressMsg{Code: 'Y', Text: "Y"}
	runs := []LogRun{{ID: "COD-1", Path: "/runs/COD-1"}}
	m := newLogsModel(DefaultStyles(), runs, 80, 24, nil)

	m, cmd := m.Update(keyShiftY, nil)
	if cmd == nil {
		t.Fatal("Y must return an OSC52 SetClipboard command")
	}
	if got := fmt.Sprintf("%s", cmd()); got != "/runs/COD-1" {
		t.Fatalf("Y must copy the run's log path, got %q", got)
	}
	if !strings.Contains(m.copied, "COD-1") || !strings.Contains(m.copied, "path") {
		t.Fatalf("Y must confirm the copied path naming the run, got %q", m.copied)
	}
	if !strings.Contains(m.View(), "copied COD-1 log path") {
		t.Fatal("the footer must show the copy-path confirmation")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}, nil)
	if m.copied != "" {
		t.Errorf("the next key must dismiss the confirmation, got %q", m.copied)
	}
}

// TestLogsCopyPathNoPath: Y is a no-op when the selected run carries no path.
func TestLogsCopyPathNoPath(t *testing.T) {
	m := newLogsModel(DefaultStyles(), []LogRun{{ID: "COD-1"}}, 80, 24, nil)
	m, cmd := m.Update(tea.KeyPressMsg{Code: 'Y', Text: "Y"}, nil)
	if cmd != nil {
		t.Fatal("Y with no path must not emit a clipboard command")
	}
	if m.copied != "" {
		t.Fatalf("Y with no path must not claim a copy, got %q", m.copied)
	}
}

// TestLogsHelpListsCopyKeys: y is in the always-on footer and Y (copy path) is
// documented in the ? overlay, so the y/Y split is discoverable.
func TestLogsHelpListsCopyKeys(t *testing.T) {
	m := newLogsModel(DefaultStyles(), nil, 80, 24, nil)
	if foot := m.help().footer(); !strings.Contains(foot, "y copy log") {
		t.Errorf("logs footer must list the copy-log key, got %q", foot)
	}
	if !helpLists(m.help(), "Y") {
		t.Error("logs ? overlay must document the copy-path key")
	}
}
