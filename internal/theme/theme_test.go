package theme

import (
	"strings"
	"testing"
)

// minimal builds a valid one-mode document with mutate applied to the raw JSON
// pieces, so each case states only what it breaks.
func minimal(mode, roles, extra string) []byte {
	body := `{"name":"T","slug":"t","modes":{"` + mode + `":{"roles":` + roles + extra + `}}}`
	return []byte(body)
}

const fullRoles = `{"brand":"#111111","accent":"#222222","success":"#333333","error":"#444444",` +
	`"warning":"#555555","info":"#666666","text":"#777777","subtle":"#888888","faint":"#999999",` +
	`"surface":"#aaaaaa","border":"#bbbbbb","ink":"#cccccc"}`

func TestParseAcceptsMinimalTheme(t *testing.T) {
	got, err := Parse(minimal(ModeDark, fullRoles, ""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Slug != "t" {
		t.Errorf("slug = %q, want t", got.Slug)
	}
	if modes := got.DefinedModes(); len(modes) != 1 || modes[0] != ModeDark {
		t.Errorf("DefinedModes = %v, want [dark]", modes)
	}
}

func TestParseRejections(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"unknown top-level key", `{"name":"T","slug":"t","palette":{},"modes":{"dark":{"roles":` + fullRoles + `}}}`, "palette"},
		{"blank name", `{"name":"","slug":"t","modes":{"dark":{"roles":` + fullRoles + `}}}`, "name"},
		{"bad slug", `{"name":"T","slug":"Nord Theme","modes":{"dark":{"roles":` + fullRoles + `}}}`, "slug"},
		{"no modes", `{"name":"T","slug":"t","modes":{}}`, "modes"},
		{"unknown mode", `{"name":"T","slug":"t","modes":{"sepia":{"roles":` + fullRoles + `}}}`, "modes.sepia"},
		{"missing role", `{"name":"T","slug":"t","modes":{"dark":{"roles":{"brand":"#111111"}}}}`, "modes.dark.roles.accent"},
		{"unknown role", `{"name":"T","slug":"t","modes":{"dark":{"roles":{"chartreuse":"#111111"}}}}`, "modes.dark.roles.chartreuse"},
		{"non-color role", `{"name":"T","slug":"t","modes":{"dark":{"roles":{"brand":"rebeccapurple","accent":"#222222","success":"#333333","error":"#444444","warning":"#555555","info":"#666666","text":"#777777","subtle":"#888888","faint":"#999999","surface":"#aaaaaa","border":"#bbbbbb","ink":"#cccccc"}}}}`, "modes.dark.roles.brand"},
		{"unknown var", `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `,"vars":{"--sparkle":"#111111"}}}}`, "modes.dark.vars.--sparkle"},
		{"non-color var", `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `,"vars":{"--ring":"var(--brand)"}}}}`, "modes.dark.vars.--ring"},
		{"unknown tui role", `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `,"tui":{"cursor":"#111111"}}}}`, "modes.dark.tui.cursor"},
		{"non-color tui role", `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `,"tui":{"brand":"green"}}}}`, "modes.dark.tui.brand"},
		{"unknown mode key", `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `,"terminal":{}}}}`, "terminal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestParseColorForms(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"#abc", "#aabbcc"},
		{"#AABBCC", "#aabbcc"},
		{"#12345678", "#12345678"},
	}
	for _, tc := range cases {
		got, err := ParseColor(tc.in)
		if err != nil {
			t.Fatalf("ParseColor(%q): %v", tc.in, err)
		}
		if got.Hex() != tc.want {
			t.Errorf("ParseColor(%q).Hex() = %q, want %q", tc.in, got.Hex(), tc.want)
		}
	}
	for _, bad := range []string{"", "abcdef", "#ab", "#abcde", "rgb(0,0,0)", "red", "#12345g"} {
		if _, err := ParseColor(bad); err == nil {
			t.Errorf("ParseColor(%q) should fail", bad)
		}
	}
}

func TestParseOverrideAcceptsBareHex(t *testing.T) {
	c, ok := ParseOverride(" 123456 ")
	if !ok || c.Hex() != "#123456" {
		t.Errorf("ParseOverride = %v %v, want #123456 true", c.Hex(), ok)
	}
	if _, ok := ParseOverride("chartreuse"); ok {
		t.Error("a color name is not an override")
	}
}

func TestBundledThemesAreValid(t *testing.T) {
	themes := Bundled()
	if len(themes) == 0 {
		t.Fatal("no bundled themes")
	}
	if themes[0].Slug != DefaultSlug {
		t.Errorf("first bundled theme = %q, want %q", themes[0].Slug, DefaultSlug)
	}
	for _, th := range themes {
		if err := th.Validate(); err != nil {
			t.Errorf("%s: %v", th.Slug, err)
		}
		if len(th.DefinedModes()) != 2 {
			t.Errorf("%s defines %v, want both modes", th.Slug, th.DefinedModes())
		}
	}
}

func TestSlugsAreDefaultFirstThenSorted(t *testing.T) {
	// Slugs reads the machine's saved themes too, so the bundled order is only
	// observable from a home that has none.
	t.Setenv("TRAU_HOME", t.TempDir())
	want := []string{"default", "catppuccin", "dracula", "gruvbox", "nord"}
	got := Slugs()
	if len(got) != len(want) {
		t.Fatalf("Slugs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Slugs() = %v, want %v", got, want)
		}
	}
}

func TestLookupNormalizesName(t *testing.T) {
	th, ok := Lookup("  NORD ")
	if !ok || th.Slug != "nord" {
		t.Errorf("Lookup = %v %v, want nord true", th.Slug, ok)
	}
	if th, ok := Lookup(""); !ok || th.Slug != DefaultSlug {
		t.Errorf("empty name should resolve the default theme, got %v %v", th.Slug, ok)
	}
	if _, ok := Lookup("sparkle"); ok {
		t.Error("an unknown name must not resolve")
	}
}
