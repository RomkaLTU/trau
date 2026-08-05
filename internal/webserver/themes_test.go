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
	if len(out.Themes) != len(theme.Slugs()) {
		t.Fatalf("themes = %d, want %d", len(out.Themes), len(theme.Slugs()))
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

func TestThemesRejectsWrites(t *testing.T) {
	home := t.TempDir()
	ts := instancesServer(t, home)
	res, err := http.Post(ts.URL+APIPrefix+"/themes", "application/json", nil)
	if err != nil {
		t.Fatalf("POST themes: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
}
