package theme

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The golden constraint of the unified format: resolving the bundled default
// theme must reproduce the palette web/src/styles.css ships, variable for
// variable, in both modes. The stylesheet is read rather than copied here so the
// two cannot drift apart silently.
func TestDefaultThemeMatchesStylesheet(t *testing.T) {
	css := readStylesheet(t)
	for _, tc := range []struct{ mode, selector string }{
		{ModeLight, ":root"},
		{ModeDark, ".dark"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			want := cssColorVars(t, css, tc.selector)
			if len(want) != len(Vars) {
				t.Fatalf("%s defines %d color variables, want %d", tc.selector, len(want), len(Vars))
			}
			got, ok := Default().ResolveWeb(tc.mode, nil)
			if !ok {
				t.Fatalf("default theme does not define %s", tc.mode)
			}
			for _, name := range Vars {
				if got[name] != want[name] {
					t.Errorf("%s: %s = %q, want %q", tc.mode, name, got[name], want[name])
				}
			}
			if len(got) != len(Vars) {
				t.Errorf("resolved %d variables, want %d", len(got), len(Vars))
			}
		})
	}
}

func TestResolveWebAppliesRoleOverridesBeforeDerivation(t *testing.T) {
	got, ok := Default().ResolveWeb(ModeDark, map[string]string{"brand": "#ff0000"})
	if !ok {
		t.Fatal("dark mode missing")
	}
	for _, name := range []string{"--primary", "--ring", "--brand"} {
		if got[name] != "#ff0000" {
			t.Errorf("%s = %q, want #ff0000", name, got[name])
		}
	}
	if got["--destructive"] == "#ff0000" {
		t.Error("an unrelated variable moved with brand")
	}
}

func TestResolveWebIgnoresUnusableOverride(t *testing.T) {
	base, _ := Default().ResolveWeb(ModeDark, nil)
	got, _ := Default().ResolveWeb(ModeDark, map[string]string{"brand": "chartreuse", "sparkle": "#ff0000"})
	if got["--primary"] != base["--primary"] {
		t.Errorf("--primary = %q, want the theme's own %q", got["--primary"], base["--primary"])
	}
}

func TestResolveWebExplicitVarsWin(t *testing.T) {
	doc := `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `,"vars":{"--ring":"#0f0f0f"}}}}`
	th, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := th.ResolveWeb(ModeDark, nil)
	if got["--ring"] != "#0f0f0f" {
		t.Errorf("--ring = %q, want the pinned #0f0f0f", got["--ring"])
	}
	if got["--primary"] != "#111111" {
		t.Errorf("--primary = %q, want the derived #111111", got["--primary"])
	}
}

func TestResolveUndefinedModeIsNotResolved(t *testing.T) {
	doc := `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `}}}`
	th, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if vars, ok := th.ResolveWeb(ModeLight, nil); ok || vars != nil {
		t.Errorf("light mode resolved to %v, want nothing", vars)
	}
	if roles, ok := th.ResolveTUI(ModeLight, nil); ok || roles != nil {
		t.Errorf("light TUI resolved to %v, want nothing", roles)
	}
}

func TestResolveTUIPrefersTheTUIBlock(t *testing.T) {
	got, ok := Default().ResolveTUI(ModeDark, nil)
	if !ok {
		t.Fatal("dark mode missing")
	}
	if got["brand"] != "#00d4aa" {
		t.Errorf("brand = %q, want the tui block's #00d4aa", got["brand"])
	}
	if len(got) != len(Roles) {
		t.Errorf("resolved %d roles, want %d", len(got), len(Roles))
	}
	over, _ := Default().ResolveTUI(ModeDark, map[string]string{"brand": "#ff0000"})
	if over["brand"] != "#ff0000" {
		t.Errorf("override brand = %q, want #ff0000", over["brand"])
	}
}

func TestResolveTUIFallsBackToRolesWithoutTUIBlock(t *testing.T) {
	doc := `{"name":"T","slug":"t","modes":{"dark":{"roles":` + fullRoles + `}}}`
	th, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := th.ResolveTUI(ModeDark, nil)
	if got["brand"] != "#111111" {
		t.Errorf("brand = %q, want the role's #111111", got["brand"])
	}
}

func TestEveryBundledThemeResolvesEveryVariable(t *testing.T) {
	for _, th := range Bundled() {
		for _, mode := range th.DefinedModes() {
			vars, ok := th.ResolveWeb(mode, nil)
			if !ok {
				t.Fatalf("%s: %s missing", th.Slug, mode)
			}
			for _, name := range Vars {
				if _, err := ParseColor(vars[name]); err != nil {
					t.Errorf("%s %s: %s = %q: %v", th.Slug, mode, name, vars[name], err)
				}
			}
		}
	}
}

// The SPA re-checks the palette it reads back out of its own cache against its
// own copy of the variable set, so a variable added here but not there would be
// dropped before it ever reached the stylesheet.
func TestWebVariableAllowlistMatches(t *testing.T) {
	path := filepath.Join("..", "..", "web", "src", "lib", "palette.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(data)
	start := strings.Index(source, "PALETTE_VARS = [")
	if start < 0 {
		t.Fatal("palette.ts has no PALETTE_VARS list")
	}
	end := strings.Index(source[start:], "]")
	if end < 0 {
		t.Fatal("PALETTE_VARS is not closed")
	}
	listed := regexp.MustCompile(`'(--[a-z-]+)'`).FindAllStringSubmatch(source[start:start+end], -1)
	if len(listed) != len(Vars) {
		t.Fatalf("palette.ts lists %d variables, want %d", len(listed), len(Vars))
	}
	for i, m := range listed {
		if m[1] != Vars[i] {
			t.Errorf("palette.ts variable %d = %q, want %q", i, m[1], Vars[i])
		}
	}
}

func readStylesheet(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "web", "src", "styles.css")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

var cssVarLine = regexp.MustCompile(`(?m)^\s*(--[a-z-]+):\s*([^;]+);`)

// cssColorVars reads the declarations of the stylesheet's first block for
// selector, keeping only the variables the theme format owns — the stylesheet
// also carries non-color tokens the format has no role for.
func cssColorVars(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	start := strings.Index(css, selector+" {")
	if start < 0 {
		t.Fatalf("stylesheet has no %s block", selector)
	}
	body := css[start+len(selector)+2:]
	end := strings.Index(body, "}")
	if end < 0 {
		t.Fatalf("%s block is not closed", selector)
	}
	out := map[string]string{}
	for _, m := range cssVarLine.FindAllStringSubmatch(body[:end], -1) {
		name, value := m[1], strings.TrimSpace(m[2])
		if !isVar(name) {
			continue
		}
		out[name] = value
	}
	return out
}
