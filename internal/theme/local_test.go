package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// saved builds a valid two-role-block document for slug, so a case states only
// the slug and name it cares about.
func saved(slug, name string) []byte {
	return []byte(`{"name":"` + name + `","slug":"` + slug + `","modes":{"dark":{"roles":` + fullRoles + `}}}`)
}

func writeTheme(t *testing.T, dir, file string, body []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestLoadLocalMissingDirectoryIsAnEmptySet(t *testing.T) {
	themes, skipped := LoadLocal(filepath.Join(t.TempDir(), "themes"))
	if len(themes) != 0 || len(skipped) != 0 {
		t.Fatalf("LoadLocal = %v, %v; want empty", themes, skipped)
	}
	if themes, skipped := LoadLocal(""); len(themes) != 0 || len(skipped) != 0 {
		t.Fatalf("LoadLocal(\"\") = %v, %v; want empty", themes, skipped)
	}
}

func TestLoadLocalReadsSavedThemesBySlug(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "zebra.json", saved("zebra", "Zebra"))
	writeTheme(t, dir, "apple.json", saved("apple", "Apple"))
	writeTheme(t, dir, "notes.txt", []byte("not a theme"))

	themes, skipped := LoadLocal(dir)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}
	if len(themes) != 2 || themes[0].Slug != "apple" || themes[1].Slug != "zebra" {
		t.Fatalf("themes = %+v, want apple then zebra", themes)
	}
}

// A theme that no longer parses costs its author a color scheme, never the hub's
// boot: the loader skips it with a note and keeps every sibling.
func TestLoadLocalSkipsUnusableFiles(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "good.json", saved("good", "Good"))
	writeTheme(t, dir, "broken.json", []byte(`{"name":"B","slug":"b","modes":{"dark":{"roles":{}}}}`))
	writeTheme(t, dir, "reserved.json", saved(DefaultSlug, "Impostor"))
	writeTheme(t, dir, "copy.json", saved("good", "Good Again"))
	writeTheme(t, dir, "huge.json", append(saved("huge", "Huge"), strings.Repeat(" ", MaxFileBytes)...))

	themes, skipped := LoadLocal(dir)
	if len(themes) != 1 || themes[0].Slug != "good" {
		t.Fatalf("themes = %+v, want only good", themes)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[filepath.Base(s.File)] = s.Reason
	}
	if len(reasons) != 4 {
		t.Fatalf("skipped = %+v, want four files", skipped)
	}
	if !strings.Contains(reasons["broken.json"], "roles") {
		t.Errorf("broken.json reason = %q, want the failing field", reasons["broken.json"])
	}
	if !strings.Contains(reasons["reserved.json"], "predefined") {
		t.Errorf("reserved.json reason = %q, want the reserved-name note", reasons["reserved.json"])
	}
	if !strings.Contains(reasons["copy.json"], "already saved") {
		t.Errorf("copy.json reason = %q, want the duplicate-slug note", reasons["copy.json"])
	}
	if !strings.Contains(reasons["huge.json"], "cap") {
		t.Errorf("huge.json reason = %q, want the size cap", reasons["huge.json"])
	}
}

func TestSaveLocalWritesAndReplaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "themes")
	first, err := Parse(saved("mine", "Mine"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	replaced, err := SaveLocal(dir, first)
	if err != nil {
		t.Fatalf("SaveLocal: %v", err)
	}
	if replaced {
		t.Error("replaced = true on the first save")
	}
	path := filepath.Join(dir, "mine.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	second, err := Parse(saved("mine", "Mine v2"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if replaced, err = SaveLocal(dir, second); err != nil || !replaced {
		t.Fatalf("SaveLocal again = %v, %v; want replaced", replaced, err)
	}
	themes, _ := LoadLocal(dir)
	if len(themes) != 1 || themes[0].Name != "Mine v2" {
		t.Fatalf("themes = %+v, want one Mine v2", themes)
	}
}

// A hand-dropped file whose name does not match its slug is still the same
// theme, so saving over it must leave one file rather than a duplicate the
// loader would then skip.
func TestSaveLocalRemovesAStaleFilenameForTheSameSlug(t *testing.T) {
	dir := t.TempDir()
	stale := writeTheme(t, dir, "hand-written.json", saved("mine", "Mine"))
	next, err := Parse(saved("mine", "Mine v2"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	replaced, err := SaveLocal(dir, next)
	if err != nil || !replaced {
		t.Fatalf("SaveLocal = %v, %v; want replaced", replaced, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file survived: %v", err)
	}
	themes, skipped := LoadLocal(dir)
	if len(themes) != 1 || len(skipped) != 0 {
		t.Fatalf("LoadLocal = %+v, %+v; want one theme and no skips", themes, skipped)
	}
}

// A slug can be held by more than one file: the editor's <slug>.json plus one
// dropped in by hand. Saving takes every holder, or the shadowed file stays
// behind forever, re-skipped on every load.
func TestSaveLocalRemovesShadowedFilesForTheSameSlug(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "mine.json", saved("mine", "Mine"))
	shadow := writeTheme(t, dir, "zz-alias.json", saved("mine", "Shadow"))
	next, err := Parse(saved("mine", "Mine v2"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	replaced, err := SaveLocal(dir, next)
	if err != nil || !replaced {
		t.Fatalf("SaveLocal = %v, %v; want replaced", replaced, err)
	}
	if _, err := os.Stat(shadow); !os.IsNotExist(err) {
		t.Errorf("the shadowed file survived the save: %v", err)
	}
	themes, skipped := LoadLocal(dir)
	if len(themes) != 1 || themes[0].Name != "Mine v2" || len(skipped) != 0 {
		t.Fatalf("LoadLocal = %+v, %+v; want one Mine v2 and no skips", themes, skipped)
	}
}

// Deleting takes every holder too: a file left behind would bring the theme
// back on the next load, painting colors the user thought they had removed.
func TestDeleteLocalRemovesShadowedFilesForTheSameSlug(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "mine.json", saved("mine", "Mine"))
	writeTheme(t, dir, "zz-alias.json", saved("mine", "Shadow"))

	removed, err := DeleteLocal(dir, "mine")
	if err != nil || !removed {
		t.Fatalf("DeleteLocal = %v, %v; want removed", removed, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("directory holds %d file(s), want none", len(entries))
	}
	themes, skipped := LoadLocal(dir)
	if len(themes) != 0 || len(skipped) != 0 {
		t.Fatalf("LoadLocal = %+v, %+v; want the theme gone", themes, skipped)
	}
}

func TestSaveLocalRefusesAPredefinedSlug(t *testing.T) {
	dir := t.TempDir()
	impostor, err := Parse(saved(DefaultSlug, "Impostor"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := SaveLocal(dir, impostor); err == nil {
		t.Fatal("want an error for a predefined slug")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("directory holds %d file(s), want none", len(entries))
	}
}

func TestDeleteLocal(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "mine.json", saved("mine", "Mine"))

	removed, err := DeleteLocal(dir, "Mine")
	if err != nil || !removed {
		t.Fatalf("DeleteLocal = %v, %v; want removed", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mine.json")); !os.IsNotExist(err) {
		t.Errorf("file survived: %v", err)
	}
	if removed, err = DeleteLocal(dir, "mine"); err != nil || removed {
		t.Fatalf("second DeleteLocal = %v, %v; want not removed", removed, err)
	}
}

// The export is the file: it parses back and resolves to the same variables,
// which is the contract a theme uploaded to trau.sh is written against.
func TestCanonicalRoundTripsThroughTheResolver(t *testing.T) {
	source := `{"name":"Round","slug":"round","author":"A","version":"2","modes":{` +
		`"light":{"roles":` + fullRoles + `,"vars":{"--ring":"#0f0f0f"},"tui":{"brand":"#010203"}},` +
		`"dark":{"roles":` + fullRoles + `}}}`
	original, err := Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, err := Canonical(original)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	exported, err := Parse(data)
	if err != nil {
		t.Fatalf("re-parse the export: %v", err)
	}
	for _, mode := range original.DefinedModes() {
		want, _ := original.ResolveWeb(mode, nil)
		got, ok := exported.ResolveWeb(mode, nil)
		if !ok {
			t.Fatalf("export lost the %s mode", mode)
		}
		for name, value := range want {
			if got[name] != value {
				t.Errorf("%s %s = %q, want %q", mode, name, got[name], value)
			}
		}
		wantTUI, _ := original.ResolveTUI(mode, nil)
		gotTUI, _ := exported.ResolveTUI(mode, nil)
		for role, value := range wantTUI {
			if gotTUI[role] != value {
				t.Errorf("%s tui %s = %q, want %q", mode, role, gotTUI[role], value)
			}
		}
	}
}

// A saved theme is a THEME value everywhere: the terminal resolves it through
// Lookup and the config catalog offers it through Slugs.
func TestLookupAndSlugsSeeSavedThemes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	writeTheme(t, LocalDir(home), "mine.json", saved("mine", "Mine"))

	got, ok := Lookup("Mine")
	if !ok || got.Name != "Mine" {
		t.Fatalf("Lookup = %+v, %v; want the saved theme", got, ok)
	}
	slugs := Slugs()
	if slugs[0] != DefaultSlug {
		t.Errorf("slugs[0] = %q, want the default first", slugs[0])
	}
	if slugs[len(slugs)-1] != "mine" {
		t.Errorf("slugs = %v, want the saved theme last", slugs)
	}
	if _, ok := Lookup("gone"); ok {
		t.Error("Lookup found a theme that is not installed")
	}
}

// A saved file cannot shadow a predefined theme, whatever it calls itself.
func TestLookupPrefersTheBundledTheme(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TRAU_HOME", home)
	writeTheme(t, LocalDir(home), "default.json", saved(DefaultSlug, "Impostor"))

	got, ok := Lookup(DefaultSlug)
	if !ok || got.Name == "Impostor" {
		t.Fatalf("Lookup(default) = %+v, %v; want the bundled default", got, ok)
	}
}
