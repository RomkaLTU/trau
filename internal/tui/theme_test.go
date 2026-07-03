package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
)

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

func TestResolveThemePresets(t *testing.T) {
	def := defaultTheme(true)
	for name := range themePresets {
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
	for name := range themePresets {
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
