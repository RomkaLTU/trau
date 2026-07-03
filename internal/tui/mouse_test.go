package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestMain initializes the global bubblezone manager once for the package, the way
// New/RunSession do in production, so any View() that runs zone.Scan has a manager.
func TestMain(m *testing.M) {
	zone.NewGlobal()
	os.Exit(m.Run())
}

var keyCtrlT = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}

// TestCopyArtifactByRowState pins the y-copy precedence: a merged row yields its
// PR (even with a stale failure reason), a faulted/quarantined row its reason
// (even with a PR), and everything else the ticket id.
func TestCopyArtifactByRowState(t *testing.T) {
	cases := []struct {
		name      string
		row       QueueRow
		wantText  string
		wantLabel string
	}{
		{"merged→PR", QueueRow{ID: "COD-3", Phase: state.Merged, PRURL: "https://gh/pr/3"}, "https://gh/pr/3", "PR URL"},
		{"merged with stale reason still PR", QueueRow{ID: "COD-3", Phase: state.Merged, PRURL: "https://gh/pr/3", FailureReason: "old transient blip"}, "https://gh/pr/3", "PR URL"},
		{"quarantined→reason over PR", QueueRow{ID: "COD-9", Phase: state.Quarantined, FailureReason: "PHPStan boom", PRURL: "https://gh/pr/9"}, "PHPStan boom", "failure reason"},
		{"faulted→reason", QueueRow{ID: "COD-8", Phase: state.HandedOff, FailureReason: "agent crashed mid-handoff"}, "agent crashed mid-handoff", "failure reason"},
		{"ready→id", QueueRow{ID: "COD-1"}, "COD-1", "ticket ID"},
		{"reset→id", QueueRow{ID: "COD-2", Phase: phaseReset}, "COD-2", "ticket ID"},
	}
	for _, c := range cases {
		text, label := copyArtifact(c.row)
		if text != c.wantText || label != c.wantLabel {
			t.Errorf("%s: copyArtifact = (%q, %q), want (%q, %q)", c.name, text, label, c.wantText, c.wantLabel)
		}
	}
}

// TestMouseToggleFlipsReporting checks ctrl+t flips the per-frame mouse mode both
// in the app shell and the standalone dashboard, so the terminal reclaims native
// drag-to-select while off.
func TestMouseToggleFlipsReporting(t *testing.T) {
	// Standalone dashboard.
	m := initialModel(nil)
	m.width, m.height = 80, 24
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("mouse should start enabled, got %v", got)
	}
	off, _, handled := m.handleKey(keyCtrlT)
	if !handled || !off.mouseOff {
		t.Fatalf("ctrl+t must toggle mouse off (handled=%v off=%v)", handled, off.mouseOff)
	}
	if got := off.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("mouse-off frame must disable reporting, got %v", got)
	}
	on, _, _ := off.handleKey(keyCtrlT)
	if on.mouseOff {
		t.Error("ctrl+t must toggle mouse back on")
	}

	// App shell: ctrl+t is global, works from a card screen too.
	app := runningApp(80, 24, nil)
	app.styles = DefaultStyles()
	next, _ := app.Update(keyCtrlT)
	napp := next.(appModel)
	if !napp.mouseOff {
		t.Fatalf("ctrl+t must toggle the app-shell mouse off")
	}
	if got := napp.View().MouseMode; got != tea.MouseModeNone {
		t.Errorf("app-shell mouse-off frame must disable reporting, got %v", got)
	}
}

// TestMouseOffOverlayIndicator checks the mouse-off frame carries a visible footer
// indicator naming the re-enable key.
func TestMouseOffOverlayIndicator(t *testing.T) {
	out := strip(overlayMouseOff(DefaultStyles(), "line one\nline two", 48, 6))
	if !strings.Contains(out, "mouse off") {
		t.Errorf("overlay must show the mouse-off indicator, got:\n%s", out)
	}
	if !strings.Contains(out, "ctrl+t") {
		t.Errorf("overlay must name the re-enable key, got:\n%s", out)
	}
}

// TestCopyKeyYanksSelectedArtifact drives y on the live rail: it sets the copy
// toast, returns an OSC52 command, and the next key dismisses the toast.
func TestCopyKeyYanksSelectedArtifact(t *testing.T) {
	keyY := tea.KeyPressMsg{Code: 'y', Text: "y"}
	rows := []QueueRow{{ID: "COD-9", Phase: state.Quarantined, FailureReason: "PHPStan boom"}}
	app := runningApp(80, 24, rows)

	next, cmd := app.Update(keyY)
	napp := next.(appModel)
	if !strings.Contains(napp.dash.toast, "failure reason") {
		t.Fatalf("y must set a copy toast naming the artifact, got %q", napp.dash.toast)
	}
	if cmd == nil {
		t.Fatal("y must return an OSC52 SetClipboard command")
	}

	after, _ := napp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := after.(appModel).dash.toast; got != "" {
		t.Errorf("the next key must dismiss the toast, got %q", got)
	}
}
