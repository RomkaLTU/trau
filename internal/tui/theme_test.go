package tui

import (
	"image/color"
	"reflect"
	"strings"
	"testing"

	colorful "github.com/lucasb-eyer/go-colorful"

	"github.com/RomkaLTU/trau/internal/config"
)

func roleHexes(th Theme) map[string]string {
	hex := func(c color.Color) string {
		col, _ := colorful.MakeColor(c)
		return col.Clamped().Hex()
	}
	return map[string]string{
		"brand": hex(th.Brand), "accent": hex(th.Accent), "success": hex(th.Success),
		"error": hex(th.Error), "warning": hex(th.Warning), "info": hex(th.Info),
		"text": hex(th.Text), "subtle": hex(th.Subtle), "faint": hex(th.Faint),
		"surface": hex(th.Surface), "border": hex(th.Border), "ink": hex(th.Ink),
	}
}

// Preset resolution moved to the unified theme format, so this pins the exact
// palette every preset rendered with before: a terminal that looked one way must
// keep looking that way, whatever the web side of the same theme does.
func TestPresetPalettesAreUnchanged(t *testing.T) {
	want := map[string]map[string]map[string]string{
		"default": {
			"light": {"brand": "#00997a", "accent": "#6c3ee8", "success": "#03875d", "error": "#d61f52", "warning": "#9a6b00", "info": "#0077c2", "text": "#16181d", "subtle": "#5f6169", "faint": "#71747e", "surface": "#e4e5e9", "border": "#c6c8cf", "ink": "#fafafa"},
			"dark":  {"brand": "#00d4aa", "accent": "#7d56f4", "success": "#04b575", "error": "#ff4672", "warning": "#f9d423", "info": "#00aaff", "text": "#ffffff", "subtle": "#888888", "faint": "#555555", "surface": "#2a2a2e", "border": "#555555", "ink": "#0b0b0e"},
		},
		"catppuccin": {
			"light": {"brand": "#179299", "accent": "#ea76cb", "success": "#40a02b", "error": "#d20f39", "warning": "#df8e1d", "info": "#1e66f5", "text": "#4c4f69", "subtle": "#85889a", "faint": "#a6a8b6", "surface": "#dfe1e7", "border": "#bec0cb", "ink": "#eff1f5"},
			"dark":  {"brand": "#94e2d5", "accent": "#f5c2e7", "success": "#a6e3a1", "error": "#f38ba8", "warning": "#f9e2af", "info": "#89b4fa", "text": "#cdd6f4", "subtle": "#9096af", "faint": "#6d7187", "surface": "#303042", "border": "#535569", "ink": "#1e1e2e"},
		},
		"dracula": {
			"light": {"brand": "#5f98a8", "accent": "#a55587", "success": "#3ca25a", "error": "#a53f43", "warning": "#9da264", "info": "#7d65a6", "text": "#1e1f29", "subtle": "#6a6b6f", "faint": "#969698", "surface": "#e2e2de", "border": "#b7b7b6", "ink": "#f8f8f2"},
			"dark":  {"brand": "#8be9fd", "accent": "#ff79c6", "success": "#50fa7b", "error": "#ff5555", "warning": "#f1fa8c", "info": "#bd93f9", "text": "#f8f8f2", "subtle": "#acacac", "faint": "#808183", "surface": "#34353d", "border": "#5f6065", "ink": "#1e1f29"},
		},
		"gruvbox": {
			"light": {"brand": "#427b58", "accent": "#8f3f71", "success": "#79740e", "error": "#9d0006", "warning": "#b57614", "info": "#076678", "text": "#282828", "subtle": "#726e60", "faint": "#9c977f", "surface": "#e6ddb7", "border": "#bcb597", "ink": "#fbf1c7"},
			"dark":  {"brand": "#578e57", "accent": "#a04b73", "success": "#868715", "error": "#be0f17", "warning": "#cc881a", "info": "#377375", "text": "#e6d4a3", "subtle": "#a09474", "faint": "#78705a", "surface": "#32302b", "border": "#5a5546", "ink": "#1e1e1e"},
		},
		"nord": {
			"light": {"brand": "#648896", "accent": "#7e6a81", "success": "#74876e", "error": "#854f59", "warning": "#9f8f6d", "info": "#60758d", "text": "#2e3440", "subtle": "#6a707b", "faint": "#8c929d", "surface": "#c7cdd8", "border": "#a5abb6", "ink": "#d8dee9"},
			"dark":  {"brand": "#88c0d0", "accent": "#b48ead", "success": "#a3be8c", "error": "#bf616a", "warning": "#ebcb8b", "info": "#81a1c1", "text": "#d8dee9", "subtle": "#9da3ae", "faint": "#7a818c", "surface": "#3f4551", "border": "#616773", "ink": "#2e3440"},
		},
	}
	for _, name := range themeNames() {
		modes, ok := want[name]
		if !ok {
			t.Fatalf("preset %q has no pinned palette; add one before shipping it", name)
		}
		for _, dark := range []bool{false, true} {
			mode := "light"
			if dark {
				mode = "dark"
			}
			th, note := resolveTheme(name, nil, dark)
			if note != "" {
				t.Errorf("%s %s: unexpected note %q", name, mode, note)
			}
			if got := roleHexes(th); !reflect.DeepEqual(got, modes[mode]) {
				t.Errorf("%s %s palette = %v, want %v", name, mode, got, modes[mode])
			}
		}
	}
}

func TestDefaultThemeAdaptsToBackground(t *testing.T) {
	dark := defaultTheme(true)
	light := defaultTheme(false)
	if reflect.DeepEqual(dark, light) {
		t.Fatal("light and dark variants are identical")
	}
	if isDarkColor(dark.Text) {
		t.Error("dark-background text should be light")
	}
	if !isDarkColor(light.Text) {
		t.Error("light-background text should be dark")
	}
	if !isDarkColor(light.Subtle) || !isDarkColor(light.Faint) {
		t.Error("light-background subtle/faint greys must stay dark enough to read")
	}
}

func presetNames() []string {
	var out []string
	for _, slug := range themeNames() {
		if slug != "default" {
			out = append(out, slug)
		}
	}
	return out
}

func TestResolveThemePresets(t *testing.T) {
	def := defaultTheme(true)
	for _, name := range presetNames() {
		th, note := resolveTheme(name, nil, true)
		if note != "" {
			t.Errorf("%s: unexpected note %q", name, note)
		}
		if reflect.DeepEqual(th, def) {
			t.Errorf("%s should differ from the default palette", name)
		}
	}
}

func TestResolveThemePresetLightVariants(t *testing.T) {
	for _, name := range presetNames() {
		th, _ := resolveTheme(name, nil, false)
		if !isDarkColor(th.Text) {
			t.Errorf("%s on a light background should use dark text", name)
		}
	}
}

func TestResolveThemeNameIsNormalized(t *testing.T) {
	want, _ := resolveTheme("nord", nil, true)
	got, note := resolveTheme("  Nord ", nil, true)
	if note != "" {
		t.Errorf("unexpected note %q", note)
	}
	if !reflect.DeepEqual(got, want) {
		t.Error("preset lookup should be case- and whitespace-insensitive")
	}
}

func TestResolveThemeUnknownFallsBack(t *testing.T) {
	th, note := resolveTheme("sparkle", nil, true)
	if !reflect.DeepEqual(th, defaultTheme(true)) {
		t.Error("unknown preset should fall back to the default palette")
	}
	if !strings.Contains(note, `"sparkle"`) || !strings.Contains(note, "default") {
		t.Errorf("note should name the bad value and the fallback, got %q", note)
	}
}

func TestResolveThemeHexOverride(t *testing.T) {
	for _, hex := range []string{"#123456", "123456"} {
		th, note := resolveTheme("default", map[string]string{"brand": hex}, true)
		if note != "" {
			t.Errorf("%s: unexpected note %q", hex, note)
		}
		r, g, b, _ := th.Brand.RGBA()
		if r>>8 != 0x12 || g>>8 != 0x34 || b>>8 != 0x56 {
			t.Errorf("%s: brand = #%02x%02x%02x, want #123456", hex, r>>8, g>>8, b>>8)
		}
	}
}

func TestResolveThemeBadOverrideIgnored(t *testing.T) {
	th, note := resolveTheme("default", map[string]string{"brand": "chartreuse"}, true)
	if !reflect.DeepEqual(th, defaultTheme(true)) {
		t.Error("invalid hex should leave the palette untouched")
	}
	if !strings.Contains(note, "THEME_BRAND") {
		t.Errorf("note should name the bad key, got %q", note)
	}
}

func TestEveryConfigThemeRoleResolves(t *testing.T) {
	def := defaultTheme(true)
	for _, role := range config.ThemeRoles {
		th, note := resolveTheme("default", map[string]string{role: "#123456"}, true)
		if note != "" {
			t.Errorf("%s: unexpected note %q", role, note)
		}
		if reflect.DeepEqual(th, def) {
			t.Errorf("override for role %q had no effect", role)
		}
	}
}

func TestThemeOptionsMatchConfigKnownKeys(t *testing.T) {
	var opts []string
	for _, k := range config.KnownKeys() {
		if k.Key == "THEME" {
			opts = k.Options
		}
	}
	if !reflect.DeepEqual(opts, themeNames()) {
		t.Errorf("THEME options %v out of sync with presets %v", opts, themeNames())
	}
}

func TestSetThemeTracksBackground(t *testing.T) {
	t.Cleanup(func() {
		SetTheme("", nil)
		setThemeBackground(true)
	})
	if note := SetTheme("gruvbox", nil); note != "" {
		t.Fatalf("unexpected note %q", note)
	}
	setThemeBackground(false)
	if !isDarkColor(theme.Text) {
		t.Error("light background should resolve the light variant")
	}
	setThemeBackground(true)
	if isDarkColor(theme.Text) {
		t.Error("dark background should resolve the dark variant")
	}
}
