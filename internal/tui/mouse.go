package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/RomkaLTU/trau/internal/state"
)

// This file is the mouse layer: the mouse-off toggle that hands drag-to-select
// back to the terminal, the OSC52 copy affordance, and the bubblezone hit-testing
// helpers the screens share. Mouse is a progressive enhancement — every action
// here has a keyboard equivalent, and turning the mouse off loses nothing.

// setMouseEnabled matches global zone hit-testing to the mouse mode, so a
// toggled-off mouse also stops the manager parsing markers it can no longer act on.
func setMouseEnabled(on bool) { zone.SetEnabled(on) }

// overlayMouseOff floats the mouse-off indicator over the bottom-right of the
// screen with the lipgloss compositor. Placing it here, over the finished frame,
// shows the mode on every screen without threading the flag through each footer.
func overlayMouseOff(s Styles, base string, w, h int) string {
	if w < 24 || h < 2 {
		return base
	}
	tag := s.Subtle.Render(" mouse off · ctrl+t to select ")
	x := w - lipgloss.Width(tag)
	if x < 0 {
		x = 0
	}
	baseLayer := lipgloss.NewLayer(padToSize(base, w, h))
	overlay := lipgloss.NewLayer(tag).X(x).Y(h - 1).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlay).Render()
}

// copyArtifact picks the most useful copyable value for a queue row and a label
// for the confirmation toast: the PR URL for a merged row, the preserved failure
// reason for a faulted one, the ticket ID otherwise. It mirrors peekContent's
// state ordering so a merged row's stale reason never wins over its PR.
func copyArtifact(r QueueRow) (text, label string) {
	reason := oneLine(r.FailureReason)
	switch {
	case r.Phase == state.Merged && r.PRURL != "":
		return r.PRURL, "PR URL"
	case r.Phase != state.Merged && r.Phase != phaseReset && reason != "":
		return reason, "failure reason"
	case r.PRURL != "":
		return r.PRURL, "PR URL"
	default:
		return r.ID, "ticket ID"
	}
}

// copySelectedArtifact copies the selected rail row's artifact to the system
// clipboard over OSC52 and sets the confirmation toast — the shared target of the
// y key on both dashboard paths and a rail row's copy click.
func (m model) copySelectedArtifact() (model, tea.Cmd) {
	sel, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	text, label := copyArtifact(sel)
	if text == "" {
		return m, nil
	}
	m.toast = "✓ copied " + label
	return m, tea.SetClipboard(text)
}

func (m model) clearToast() model {
	m.toast = ""
	return m
}
