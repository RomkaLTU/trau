package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/theme"
)

const editorRoles = `{"brand":"#7d56f4","accent":"#22d3ee","success":"#16a34a","error":"#dc2626",` +
	`"warning":"#d97706","info":"#2563eb","text":"#111111","subtle":"#666666","faint":"#999999",` +
	`"surface":"#eeeeee","border":"#dddddd","ink":"#ffffff"}`

func draftDoc(slug, name string) string {
	return `{"name":"` + name + `","slug":"` + slug + `","modes":{"light":{"roles":` + editorRoles + `}}}`
}

func postThemeJSON(t *testing.T, ts *httptest.Server, path, body string) (int, string) {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func deleteTheme(t *testing.T, ts *httptest.Server, slug string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+APIPrefix+"/themes/"+slug, nil)
	if err != nil {
		t.Fatalf("build DELETE: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE theme: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func getRaw(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}

func themeFiles(t *testing.T, home string) []string {
	t.Helper()
	entries, err := os.ReadDir(theme.LocalDir(home))
	if os.IsNotExist(err) {
		return []string{}
	}
	if err != nil {
		t.Fatalf("read themes dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestThemeSaveJoinsTheRoster(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	status, body := postThemeJSON(t, ts, "/themes", draftDoc("my-theme", "My Theme"))
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", status, body)
	}
	var saved ThemeSaveResponse
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if saved.Replaced {
		t.Error("replaced = true on a fresh slug")
	}
	if saved.Theme.Origin != theme.OriginLocal {
		t.Errorf("origin = %q, want %q", saved.Theme.Origin, theme.OriginLocal)
	}
	if files := themeFiles(t, home); len(files) != 1 || files[0] != "my-theme.json" {
		t.Fatalf("themes dir = %v, want my-theme.json", files)
	}

	out := getThemes(t, ts, "")
	var view *ThemeView
	for i := range out.Themes {
		if out.Themes[i].Slug == "my-theme" {
			view = &out.Themes[i]
		}
	}
	if view == nil {
		t.Fatalf("roster is missing my-theme: %+v", out.Themes)
	}
	if view.Origin != theme.OriginLocal || view.Roles[theme.ModeLight]["brand"] != "#7d56f4" {
		t.Errorf("roster entry = %+v, want a local theme carrying its brand", view)
	}
}

// Activating a saved theme is an ordinary THEME write, so the hub must resolve
// the slug the same way the terminal does.
func TestThemeSavedThenActivated(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	if status, body := postThemeJSON(t, ts, "/themes", draftDoc("my-theme", "My Theme")); status != http.StatusCreated {
		t.Fatalf("save status = %d (%s)", status, body)
	}
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("THEME=my-theme\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}

	out := getThemes(t, ts, "?repo=acme")
	if out.Active != "my-theme" {
		t.Fatalf("active = %q, want my-theme", out.Active)
	}
	if out.Note != "" {
		t.Errorf("note = %q, want none", out.Note)
	}
	if got := out.Resolved[theme.ModeLight]["--primary"]; got != "#7d56f4" {
		t.Errorf("light --primary = %q, want #7d56f4", got)
	}
	if _, ok := out.Resolved[theme.ModeDark]; ok {
		t.Error("resolved carries a dark mode the theme does not define")
	}
}

func TestThemeSaveRejectionsStoreNothing(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	cases := []struct {
		name   string
		body   string
		status int
		names  string
	}{
		{
			name:   "predefined slug is reserved",
			body:   draftDoc(theme.DefaultSlug, "Impostor"),
			status: http.StatusConflict,
			names:  "predefined",
		},
		{
			name:   "the draft resolver's own path is reserved",
			body:   draftDoc("resolve", "Resolve"),
			status: http.StatusConflict,
			names:  "reserved",
		},
		{
			name:   "missing role",
			body:   `{"name":"T","slug":"t","modes":{"light":{"roles":{"brand":"#111111"}}}}`,
			status: http.StatusBadRequest,
			names:  "modes.light.roles.accent",
		},
		{
			name:   "non-color role",
			body:   strings.Replace(draftDoc("t", "T"), `"brand":"#7d56f4"`, `"brand":"rebeccapurple"`, 1),
			status: http.StatusBadRequest,
			names:  "modes.light.roles.brand",
		},
		{
			name:   "unknown mode",
			body:   strings.Replace(draftDoc("t", "T"), `"light"`, `"sepia"`, 1),
			status: http.StatusBadRequest,
			names:  "modes.sepia",
		},
		{
			name:   "over the size cap",
			body:   strings.Replace(draftDoc("t", "T"), `"name":"T"`, `"name":"`+strings.Repeat("T", theme.MaxFileBytes)+`"`, 1),
			status: http.StatusRequestEntityTooLarge,
			names:  "cap",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postThemeJSON(t, ts, "/themes", tc.body)
			if status != tc.status {
				t.Fatalf("status = %d, want %d (%s)", status, tc.status, body)
			}
			if !strings.Contains(body, tc.names) {
				t.Errorf("error %s does not name %q", body, tc.names)
			}
		})
	}
	if files := themeFiles(t, home); len(files) != 0 {
		t.Errorf("themes dir = %v, want nothing stored", files)
	}
}

func TestThemeSaveOverwritesAnExistingLocalTheme(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	if status, body := postThemeJSON(t, ts, "/themes", draftDoc("my-theme", "My Theme")); status != http.StatusCreated {
		t.Fatalf("first save = %d (%s)", status, body)
	}
	status, body := postThemeJSON(t, ts, "/themes", draftDoc("my-theme", "My Theme v2"))
	if status != http.StatusOK {
		t.Fatalf("second save = %d, want 200 (%s)", status, body)
	}
	var saved ThemeSaveResponse
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if !saved.Replaced {
		t.Error("replaced = false when the slug was already saved")
	}
	if files := themeFiles(t, home); len(files) != 1 {
		t.Errorf("themes dir = %v, want one file", files)
	}
}

func TestThemeDeleteFallsBackToTheDefault(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	if status, body := postThemeJSON(t, ts, "/themes", draftDoc("my-theme", "My Theme")); status != http.StatusCreated {
		t.Fatalf("save status = %d (%s)", status, body)
	}
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("THEME=my-theme\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}

	if status, body := deleteTheme(t, ts, "my-theme"); status != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", status, body)
	}
	if _, err := os.Stat(filepath.Join(theme.LocalDir(home), "my-theme.json")); !os.IsNotExist(err) {
		t.Errorf("theme file survived the delete: %v", err)
	}

	out := getThemes(t, ts, "?repo=acme")
	if out.Active != theme.DefaultSlug {
		t.Errorf("active = %q, want the default", out.Active)
	}
	if !strings.Contains(out.Note, "my-theme") {
		t.Errorf("note = %q, want it to name the missing theme", out.Note)
	}
	if out.Resolved[theme.ModeDark]["--brand"] != "#ff7a18" {
		t.Errorf("dark --brand = %q, want the built-in", out.Resolved[theme.ModeDark]["--brand"])
	}
}

func TestThemeDeleteRefusesPredefinedAndUnknown(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	status, body := deleteTheme(t, ts, theme.DefaultSlug)
	if status != http.StatusConflict {
		t.Fatalf("delete default = %d, want 409 (%s)", status, body)
	}
	if status, body = deleteTheme(t, ts, "nord"); status != http.StatusConflict {
		t.Fatalf("delete nord = %d, want 409 (%s)", status, body)
	}
	if status, body = deleteTheme(t, ts, "never-saved"); status != http.StatusNotFound {
		t.Fatalf("delete unknown = %d, want 404 (%s)", status, body)
	}
}

// The editor previews from the hub's own derivation, so a draft resolves without
// ever being saved — and a draft that breaks the format is refused by field.
func TestThemeResolveDraft(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	status, body := postThemeJSON(t, ts, "/themes/resolve", draftDoc("draft", "Draft"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", status, body)
	}
	var out ThemeResolveResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if len(out.Modes) != 1 || out.Modes[0] != theme.ModeLight {
		t.Fatalf("modes = %v, want [light]", out.Modes)
	}
	if len(out.Resolved[theme.ModeLight]) != len(theme.Vars) {
		t.Errorf("light resolved %d variables, want %d", len(out.Resolved[theme.ModeLight]), len(theme.Vars))
	}
	if out.Resolved[theme.ModeLight]["--ring"] != "#7d56f4" {
		t.Errorf("--ring = %q, want the draft's brand", out.Resolved[theme.ModeLight]["--ring"])
	}
	if files := themeFiles(t, home); len(files) != 0 {
		t.Errorf("resolving a draft stored %v", files)
	}

	if status, body = postThemeJSON(t, ts, "/themes/resolve", `{"name":"T","slug":"t","modes":{}}`); status != http.StatusBadRequest {
		t.Fatalf("invalid draft = %d, want 400 (%s)", status, body)
	}
}

// A THEME_<ROLE> override is this repo's resolution of a theme, not part of the
// theme, so it must not reach the draft the editor is previewing.
func TestThemeResolveIgnoresRoleOverrides(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("THEME_BRAND=#ff0000\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}
	ts := instancesServer(t, home)

	_, body := postThemeJSON(t, ts, "/themes/resolve?repo=acme", draftDoc("draft", "Draft"))
	var out ThemeResolveResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if got := out.Resolved[theme.ModeLight]["--brand"]; got != "#7d56f4" {
		t.Errorf("--brand = %q, want the draft's own #7d56f4", got)
	}
}

// The exported file is the shareable theme: it must re-resolve to exactly what
// the theme it came from resolves to, pinned vars and tui block included.
func TestThemeExportRoundTripsThroughResolve(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)

	source := `{"name":"Round Trip","slug":"round-trip","author":"QA","version":"3","modes":{` +
		`"light":{"roles":` + editorRoles + `,"vars":{"--ring":"#0f0f0f"},"tui":{"brand":"#010203"}},` +
		`"dark":{"roles":` + editorRoles + `}}}`
	if status, body := postThemeJSON(t, ts, "/themes", source); status != http.StatusCreated {
		t.Fatalf("save status = %d (%s)", status, body)
	}

	status, exported := getRaw(t, ts, "/themes/round-trip")
	if status != http.StatusOK {
		t.Fatalf("export status = %d, want 200 (%s)", status, exported)
	}
	if !json.Valid([]byte(exported)) {
		t.Fatalf("export is not valid JSON: %s", exported)
	}

	_, before := postThemeJSON(t, ts, "/themes/resolve", source)
	_, after := postThemeJSON(t, ts, "/themes/resolve", exported)
	var want, got ThemeResolveResponse
	if err := json.Unmarshal([]byte(before), &want); err != nil {
		t.Fatalf("decode source resolve: %v", err)
	}
	if err := json.Unmarshal([]byte(after), &got); err != nil {
		t.Fatalf("decode export resolve: %v", err)
	}
	if !bytes.Equal(marshalTheme(t, want), marshalTheme(t, got)) {
		t.Errorf("resolve(export) = %s, want %s", marshalTheme(t, got), marshalTheme(t, want))
	}

	if status, body := getRaw(t, ts, "/themes/never-saved"); status != http.StatusNotFound {
		t.Fatalf("export unknown = %d, want 404 (%s)", status, body)
	}
}

func marshalTheme(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// A theme file that no longer parses is skipped, never fatal: the hub still
// serves every sibling and the built-in set.
func TestThemesSkipUnusableSavedFiles(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	dir := theme.LocalDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir themes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"nope":true}`), 0o644); err != nil {
		t.Fatalf("write broken theme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(draftDoc("good", "Good")), 0o644); err != nil {
		t.Fatalf("write good theme: %v", err)
	}
	ts := instancesServer(t, home)

	out := getThemes(t, ts, "")
	slugs := map[string]bool{}
	for _, view := range out.Themes {
		slugs[view.Slug] = true
	}
	if !slugs["good"] || slugs["broken"] {
		t.Fatalf("roster = %v, want good without broken", slugs)
	}
	if !slugs[theme.DefaultSlug] {
		t.Error("roster lost the bundled set")
	}
}
