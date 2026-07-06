package tui

import (
	"os"
	"strings"
	"testing"
)

// TestBrandMark covers the "⬡ <repo>" header mark: the resolved repo when set,
// the bare brand as a fallback, and the cap against pathological folder names.
func TestBrandMark(t *testing.T) {
	defer setScreenRepo("")

	setScreenRepo("salonradar")
	if got := brandMark(); got != "⬡ salonradar" {
		t.Errorf("brandMark() = %q, want %q", got, "⬡ salonradar")
	}

	setScreenRepo("")
	if got := brandMark(); got != "⬡ trau" {
		t.Errorf("empty brandMark() = %q, want the bare brand", got)
	}

	setScreenRepo(strings.Repeat("x", 60))
	if w := len([]rune(strings.TrimPrefix(brandMark(), "⬡ "))); w > repoNameMax {
		t.Errorf("repo name not capped: %d runes in %q", w, brandMark())
	}
}

// TestCardViewRepoTag verifies every card screen pins the subtle "⬡ <repo>" tag on
// the top-left of the viewport, outside the centered card, and shows none when the
// repo is unknown.
func TestCardViewRepoTag(t *testing.T) {
	defer setScreenRepo("")
	s := DefaultStyles()

	setScreenRepo("salonradar")
	first := strings.Split(cardView(s, 80, 20, "body", "esc back"), "\n")[0]
	if !strings.Contains(first, "⬡ salonradar") {
		t.Errorf("corner tag missing from top-left line: %q", first)
	}

	setScreenRepo("")
	if got := cardView(s, 80, 20, "body", "esc back"); strings.Contains(got, "⬡") {
		t.Error("no repo tag should render when the repo is unknown")
	}
}

// TestAbbreviateHome rewrites a home-relative path as ~, leaving other paths alone.
func TestAbbreviateHome(t *testing.T) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if got := abbreviateHome(home + "/Herd/salonradar"); got != "~/Herd/salonradar" {
			t.Errorf("abbreviateHome(home/Herd/salonradar) = %q", got)
		}
		if got := abbreviateHome(home); got != "~" {
			t.Errorf("abbreviateHome(home) = %q, want ~", got)
		}
	}
	if got := abbreviateHome("/opt/work/salonradar"); got != "/opt/work/salonradar" {
		t.Errorf("abbreviateHome mangled a non-home path: %q", got)
	}
	if got := abbreviateHome(""); got != "" {
		t.Errorf("abbreviateHome(\"\") = %q, want empty", got)
	}
}

// TestMenuShowsRepoPath checks the menu's context block carries the repo path row
// (spec item 5) and inherits the corner tag.
func TestMenuShowsRepoPath(t *testing.T) {
	defer setScreenRepo("")
	setScreenRepo("salonradar")

	m := appModel{
		styles: DefaultStyles(),
		width:  80,
		height: 40,
		info:   MenuInfo{Version: "1.6.0", RepoPath: "/opt/work/salonradar", Provider: "claude", Base: "main"},
	}
	menu := m.renderMenu()
	if !strings.Contains(menu, "/opt/work/salonradar") {
		t.Error("menu missing the repo path row")
	}
	if !strings.Contains(menu, "⬡ salonradar") {
		t.Error("menu missing the corner tag")
	}
}
