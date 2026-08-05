package webserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/theme"
)

func getThemes(t *testing.T, ts *httptest.Server, query string) ThemesResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/themes" + query)
	if err != nil {
		t.Fatalf("GET themes: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.StatusCode, body)
	}
	var out ThemesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode themes: %v", err)
	}
	return out
}

func TestThemesListsBundledSetAndResolvesDefault(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	out := getThemes(t, ts, "")
	if out.Active != theme.DefaultSlug {
		t.Errorf("active = %q, want %q", out.Active, theme.DefaultSlug)
	}
	// The server's home is a temp dir with no saved themes, so the listing is
	// the bundled set — not this machine's, which Slugs would fold in.
	if len(out.Themes) != len(theme.Bundled()) {
		t.Fatalf("themes = %d, want %d", len(out.Themes), len(theme.Bundled()))
	}
	for _, view := range out.Themes {
		if view.Origin != theme.OriginBundled {
			t.Errorf("%s origin = %q, want %q", view.Slug, view.Origin, theme.OriginBundled)
		}
		if view.Name == "" || len(view.Modes) == 0 {
			t.Errorf("%s carries no name/modes: %+v", view.Slug, view)
		}
	}
	for _, mode := range []string{theme.ModeLight, theme.ModeDark} {
		vars, ok := out.Resolved[mode]
		if !ok {
			t.Fatalf("resolved is missing %s", mode)
		}
		if len(vars) != len(theme.Vars) {
			t.Errorf("%s resolved %d variables, want %d", mode, len(vars), len(theme.Vars))
		}
	}
	if out.Resolved[theme.ModeDark]["--brand"] != "#ff7a18" {
		t.Errorf("dark --brand = %q, want the built-in #ff7a18", out.Resolved[theme.ModeDark]["--brand"])
	}
}

// A swatch shows the theme, not the repo's resolution of it, so the seeded
// THEME_BRAND override must not reach any card's brand color.
func TestThemesCarryTheirSwatchRoles(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("THEME_BRAND=#ff0000\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}
	ts := instancesServer(t, home)

	bySlug := map[string]ThemeView{}
	for _, view := range getThemes(t, ts, "?repo=acme").Themes {
		bySlug[view.Slug] = view
	}

	cases := []struct {
		slug string
		mode string
		role string
		want string
	}{
		{"nord", theme.ModeDark, "surface", "#3b4252"},
		{"nord", theme.ModeDark, "brand", "#88c0d0"},
		{"nord", theme.ModeLight, "text", "#2e3440"},
		{"gruvbox", theme.ModeLight, "accent", "#8f3f71"},
		{"gruvbox", theme.ModeDark, "error", "#fb4934"},
		{"default", theme.ModeDark, "brand", "#ff7a18"},
		{"default", theme.ModeLight, "success", "#0f9d63"},
	}
	for _, tc := range cases {
		view, ok := bySlug[tc.slug]
		if !ok {
			t.Fatalf("themes is missing %s", tc.slug)
		}
		if got := view.Roles[tc.mode][tc.role]; got != tc.want {
			t.Errorf("%s %s.%s = %q, want %q", tc.slug, tc.mode, tc.role, got, tc.want)
		}
	}

	for slug, view := range bySlug {
		if len(view.Roles) != len(view.Modes) {
			t.Errorf("%s carries %d role sets for %d modes", slug, len(view.Roles), len(view.Modes))
		}
		for _, mode := range view.Modes {
			roles := view.Roles[mode]
			if len(roles) != len(theme.Roles) {
				t.Errorf("%s %s carries %d roles, want %d", slug, mode, len(roles), len(theme.Roles))
			}
			for _, role := range theme.Roles {
				if _, err := theme.ParseColor(roles[role]); err != nil {
					t.Errorf("%s %s.%s: %v", slug, mode, role, err)
				}
			}
		}
	}
}

func TestThemesFollowsTheRepoConfig(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	seed := "THEME=nord\nTHEME_BRAND=#ff0000\n"
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}
	ts := instancesServer(t, home)

	out := getThemes(t, ts, "?repo=acme")
	if out.Active != "nord" {
		t.Fatalf("active = %q, want nord", out.Active)
	}
	dark := out.Resolved[theme.ModeDark]
	if dark["--background"] != "#2e3440" {
		t.Errorf("dark --background = %q, want nord's #2e3440", dark["--background"])
	}
	for _, name := range []string{"--primary", "--ring", "--brand"} {
		if dark[name] != "#ff0000" {
			t.Errorf("%s = %q, want the THEME_BRAND override #ff0000", name, dark[name])
		}
	}
	if unscoped := getThemes(t, ts, ""); unscoped.Active != theme.DefaultSlug {
		t.Errorf("unscoped active = %q, want %q", unscoped.Active, theme.DefaultSlug)
	}
}

func TestThemesUnknownNameFallsBackToDefault(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("THEME=sparkle\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}
	ts := instancesServer(t, home)

	if out := getThemes(t, ts, "?repo=acme"); out.Active != theme.DefaultSlug {
		t.Errorf("active = %q, want %q", out.Active, theme.DefaultSlug)
	}
}

// The collection reads and saves; everything else on it is a method error, and
// the draft resolver only ever takes a POST.
func TestThemesRejectsUnsupportedMethods(t *testing.T) {
	home := t.TempDir()
	ts := instancesServer(t, home)
	for _, path := range []string{"/themes", "/themes/resolve"} {
		req, err := http.NewRequest(http.MethodPut, ts.URL+APIPrefix+path, nil)
		if err != nil {
			t.Fatalf("build PUT %s: %v", path, err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s: %v", path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("PUT %s = %d, want 405", path, res.StatusCode)
		}
	}
}
