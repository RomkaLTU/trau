package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	tint "github.com/lrstanley/bubbletint"
	colorful "github.com/lucasb-eyer/go-colorful"
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

func defaultTheme(isDark bool) Theme {
	ld := lipgloss.LightDark(isDark)
	return Theme{
		Brand:   ld(lipgloss.Color("#00997A"), lipgloss.Color("#00D4AA")),
		Accent:  ld(lipgloss.Color("#6C3EE8"), lipgloss.Color("#7D56F4")),
		Success: ld(lipgloss.Color("#03875D"), lipgloss.Color("#04B575")),
		Error:   ld(lipgloss.Color("#D61F52"), lipgloss.Color("#FF4672")),
		Warning: ld(lipgloss.Color("#9A6B00"), lipgloss.Color("#F9D423")),
		Info:    ld(lipgloss.Color("#0077C2"), lipgloss.Color("#00AAFF")),
		Text:    ld(lipgloss.Color("#16181D"), lipgloss.Color("#FFFFFF")),
		Subtle:  ld(lipgloss.Color("#5F6169"), lipgloss.Color("#888888")),
		Faint:   ld(lipgloss.Color("#71747E"), lipgloss.Color("#555555")),
		Surface: ld(lipgloss.Color("#E4E5E9"), lipgloss.Color("#2A2A2E")),
		Border:  ld(lipgloss.Color("#C6C8CF"), lipgloss.Color("#555555")),
		Ink:     ld(lipgloss.Color("#FAFAFA"), lipgloss.Color("#0B0B0E")),
	}
}

// themePresets pairs each selectable preset with the bubbletint variant used
// per background polarity. Presets without a light variant reuse the dark tint;
// themeFromTint keeps them legible by swapping fg/bg and dimming chromatics.
var themePresets = map[string]struct{ dark, light tint.Tint }{
	"catppuccin": {tint.TintCatppuccinMocha, tint.TintCatppuccinLatte},
	"dracula":    {tint.TintDracula, tint.TintDracula},
	"gruvbox":    {tint.TintGruvboxDark, tint.TintGruvboxLight},
	"nord":       {tint.TintNord, tint.TintNord},
}

func themeNames() []string {
	presets := make([]string, 0, len(themePresets))
	for name := range themePresets {
		presets = append(presets, name)
	}
	sort.Strings(presets)
	return append([]string{"default"}, presets...)
}

// resolveTheme maps a preset name plus per-role hex overrides onto the palette
// for the given background polarity. Invalid names or overrides fall back and
// are reported in the returned note.
func resolveTheme(name string, overrides map[string]string, isDark bool) (Theme, string) {
	var notes []string
	th := defaultTheme(isDark)
	switch key := strings.ToLower(strings.TrimSpace(name)); key {
	case "", "default":
	default:
		if p, ok := themePresets[key]; ok {
			t := p.dark
			if !isDark {
				t = p.light
			}
			th = themeFromTint(t, isDark)
		} else {
			notes = append(notes, fmt.Sprintf("unknown THEME %q — using default (themes: %s)",
				name, strings.Join(themeNames(), ", ")))
		}
	}
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

func themeFromTint(t tint.Tint, isDark bool) Theme {
	fg, bg := tintColor(t.Fg()), tintColor(t.Bg())
	mismatch := isDarkColor(bg) != isDark
	if mismatch {
		fg, bg = bg, fg
	}
	th := Theme{
		Brand:   tintColor(t.Cyan()),
		Accent:  tintColor(t.Purple()),
		Success: tintColor(t.Green()),
		Error:   tintColor(t.Red()),
		Warning: tintColor(t.Yellow()),
		Info:    tintColor(t.Blue()),
		Text:    fg,
		Subtle:  blendColor(fg, bg, 0.35),
		Faint:   blendColor(fg, bg, 0.55),
		Surface: blendColor(bg, fg, 0.10),
		Border:  blendColor(bg, fg, 0.30),
		Ink:     bg,
	}
	if mismatch {
		for _, c := range []*color.Color{&th.Brand, &th.Accent, &th.Success, &th.Error, &th.Warning, &th.Info} {
			*c = blendColor(*c, fg, 0.4)
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

// tintColor converts a bubbletint color to a plain color.Color by its hex
// string; bubbletint's own RGBA() goes through a terminal color profile and
// collapses to black on non-tty outputs.
func tintColor(c any) color.Color {
	col, _ := colorful.Hex(strings.ToLower(fmt.Sprintf("%v", c)))
	return col
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

func blendColor(from, to color.Color, frac float64) color.Color {
	f, _ := colorful.MakeColor(from)
	g, _ := colorful.MakeColor(to)
	return f.BlendRgb(g, frac).Clamped()
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
