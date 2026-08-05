package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	colorful "github.com/lucasb-eyer/go-colorful"

	themedef "github.com/RomkaLTU/trau/internal/theme"
)

// Theme is the semantic palette every screen draws from. Styles reference
// roles, never raw hex values, so resolving once against the terminal
// background (and any preset/overrides) adapts the whole UI.
type Theme struct {
	Brand   color.Color
	Accent  color.Color
	Success color.Color
	Error   color.Color
	Warning color.Color
	Info    color.Color
	Text    color.Color
	Subtle  color.Color
	Faint   color.Color
	Surface color.Color
	Border  color.Color
	// Ink is the text drawn on top of chromatic fills (chips, selected rows).
	Ink color.Color
}

// defaultTheme is the built-in palette for a background polarity, resolved from
// the same theme document the web UI derives its variables from.
func defaultTheme(isDark bool) Theme {
	return themeFromDefinition(themedef.Default(), isDark)
}

func themeNames() []string { return themedef.Slugs() }

// resolveTheme maps a preset name plus per-role hex overrides onto the palette
// for the given background polarity. Invalid names or overrides fall back and
// are reported in the returned note.
func resolveTheme(name string, overrides map[string]string, isDark bool) (Theme, string) {
	var notes []string
	def, ok := themedef.Lookup(name)
	if !ok {
		notes = append(notes, fmt.Sprintf("unknown THEME %q — using default (themes: %s)",
			name, strings.Join(themeNames(), ", ")))
		def = themedef.Default()
	}
	th := themeFromDefinition(def, isDark)
	for _, role := range sortedKeys(overrides) {
		c, ok := parseHexColor(overrides[role])
		if !ok {
			notes = append(notes, fmt.Sprintf("THEME_%s %q is not a hex color — ignored",
				strings.ToUpper(role), overrides[role]))
			continue
		}
		th.setRole(role, c)
	}
	return th, strings.Join(notes, "; ")
}

// themeFromDefinition resolves one polarity of a theme document into the
// palette. A theme that does not define this mode leaves the built-in palette in
// place rather than borrowing its other mode, which would paint a light terminal
// with dark-mode colors.
func themeFromDefinition(def themedef.Theme, isDark bool) Theme {
	mode := themedef.ModeName(isDark)
	roles, ok := def.ResolveTUI(mode, nil)
	if !ok {
		roles, _ = themedef.Default().ResolveTUI(mode, nil)
	}
	var th Theme
	for role, hex := range roles {
		if c, ok := parseHexColor(hex); ok {
			th.setRole(role, c)
		}
	}
	return th
}

func (t *Theme) setRole(role string, c color.Color) {
	switch strings.ToLower(role) {
	case "brand":
		t.Brand = c
	case "accent":
		t.Accent = c
	case "success":
		t.Success = c
	case "error":
		t.Error = c
	case "warning":
		t.Warning = c
	case "info":
		t.Info = c
	case "text":
		t.Text = c
	case "subtle":
		t.Subtle = c
	case "faint":
		t.Faint = c
	case "surface":
		t.Surface = c
	case "border":
		t.Border = c
	case "ink":
		t.Ink = c
	}
}

func parseHexColor(s string) (color.Color, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	if !strings.HasPrefix(s, "#") {
		s = "#" + s
	}
	c, err := colorful.Hex(strings.ToLower(s))
	if err != nil {
		return nil, false
	}
	return c, true
}

func isDarkColor(c color.Color) bool {
	col, _ := colorful.MakeColor(c)
	_, _, l := col.Hsl()
	return l < 0.5
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
