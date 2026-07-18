package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// This file is the persistent web hub indicator: "Web ✓ :8728" / "Web ✗"
// pinned top-right of every card screen and woven into the run header's right
// cluster. Like screenRepo it is package state the kit reads: the session
// shell probes the hub on its existing spinner tick and writes the result via
// setScreenWeb; a session that never wires a hub (the standalone dashboard)
// leaves it unset and renders no indicator.

// webStatus is the hub's reachability as the indicator shows it: the hub
// origin from config plus whether it answered the last health probe.
type webStatus struct {
	base    string
	healthy bool
}

var screenWeb webStatus

func setScreenWeb(w webStatus) { screenWeb = w }

func (w webStatus) known() bool { return w.base != "" }

// port is the ":8728" tail of the hub origin. hubBaseURL always carries a
// port, so the last colon is the host:port separator even for IPv6.
func (w webStatus) port() string {
	if i := strings.LastIndexByte(w.base, ':'); i > 0 {
		return w.base[i:]
	}
	return ""
}

// label is the indicator text: "Web ✓ :8728" up, "Web ✗" down, "" unknown.
func (w webStatus) label() string {
	if !w.known() {
		return ""
	}
	if w.healthy {
		return "Web ✓ " + w.port()
	}
	return "Web ✗"
}

// webStatusChip is the run header's form of the indicator, colored by health.
func webStatusChip() string {
	label := screenWeb.label()
	if label == "" {
		return ""
	}
	c := theme.Success
	if !screenWeb.healthy {
		c = theme.Error
	}
	return chip(label, c)
}

// overlayWebStatus pins the indicator top-right of a non-running screen,
// extended with the transient Open Web UI outcome while one is set, so a
// refused or failed open is spelled out where the ✗ appears.
func overlayWebStatus(s Styles, base, note string, noteErr bool, w, h int) string {
	label := screenWeb.label()
	if label == "" {
		if note == "" {
			return base
		}
		label = "Web ✗"
	}
	style := s.Subtle
	if !screenWeb.healthy {
		style = s.Error
	}
	tag := style.Render(label)
	if room := w - lipgloss.Width(tag) - 8; note != "" && room >= 8 {
		noteStyle := s.Subtle
		if noteErr {
			noteStyle = s.Error
		}
		tag += " " + noteStyle.Render("— "+truncate(note, room))
	}
	if w < lipgloss.Width(tag) || h < 1 {
		return base
	}
	baseLayer := lipgloss.NewLayer(padToSize(base, w, h))
	overlay := lipgloss.NewLayer(tag).X(w - lipgloss.Width(tag)).Y(0).Z(1)
	return lipgloss.NewCompositor(baseLayer, overlay).Render()
}
