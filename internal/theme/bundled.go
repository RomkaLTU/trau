package theme

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
)

// DefaultSlug is the theme every fail-soft path lands on: an unset THEME, an
// unknown name, a mode a theme does not define.
const DefaultSlug = "default"

// OriginBundled marks a theme compiled into the binary, as opposed to one saved
// on this machine.
const OriginBundled = "bundled"

//go:embed themes/*.json
var themeFiles embed.FS

// bundled parses the embedded set once. The files ship inside the binary and a
// test parses every one of them, so a failure here means a corrupt build rather
// than bad input, and panicking beats painting an unthemed UI in silence.
var bundled = sync.OnceValue(loadBundled)

func loadBundled() []Theme {
	entries, err := themeFiles.ReadDir("themes")
	if err != nil {
		panic(fmt.Sprintf("theme: read embedded themes: %v", err))
	}
	out := make([]Theme, 0, len(entries))
	for _, entry := range entries {
		name := path.Join("themes", entry.Name())
		data, err := themeFiles.ReadFile(name)
		if err != nil {
			panic(fmt.Sprintf("theme: read %s: %v", name, err))
		}
		t, err := Parse(data)
		if err != nil {
			panic(fmt.Sprintf("theme: %s: %v", name, err))
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Slug == DefaultSlug) != (out[j].Slug == DefaultSlug) {
			return out[i].Slug == DefaultSlug
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// Bundled returns the themes compiled into the binary, default first and the
// rest by slug — the order the settings catalog and the API both present.
func Bundled() []Theme {
	src := bundled()
	out := make([]Theme, len(src))
	copy(out, src)
	return out
}

// Slugs lists the theme names a THEME value may take on this machine: the
// bundled set in Bundled order, then the saved ones by slug. It is what the
// config catalog offers as options and what the terminal names when THEME is
// unknown, so a theme saved from the editor is a valid THEME everywhere.
func Slugs() []string {
	src := bundled()
	local, _ := Local()
	out := make([]string, 0, len(src)+len(local))
	for _, t := range src {
		out = append(out, t.Slug)
	}
	for _, t := range local {
		out = append(out, t.Slug)
	}
	return out
}

// Normalize reads a THEME value the way config values are read everywhere else —
// trimmed and lowercased — and resolves an empty name to the default theme.
func Normalize(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	if slug == "" {
		return DefaultSlug
	}
	return slug
}

// Lookup resolves a configured THEME value against the bundled themes first and
// the ones saved on this machine second, so a predefined name always means the
// theme trau ships whatever sits in the themes directory. A name neither set
// carries is not found, and every caller falls back to the default with a note.
func Lookup(name string) (Theme, bool) {
	if t, ok := LookupBundled(name); ok {
		return t, true
	}
	local, _ := Local()
	return findSlug(local, Normalize(name))
}

// LookupIn is Lookup against a named themes directory, for a caller — the hub —
// that keeps its own trau home rather than the machine's.
func LookupIn(dir, name string) (Theme, bool) {
	if t, ok := LookupBundled(name); ok {
		return t, true
	}
	local, _ := LoadLocal(dir)
	return findSlug(local, Normalize(name))
}

// LookupBundled resolves a name against the themes compiled into the binary
// only. It is what makes predefined slugs reserved: a save or a delete asks
// this before it touches the themes directory.
func LookupBundled(name string) (Theme, bool) {
	return findSlug(bundled(), Normalize(name))
}

func findSlug(themes []Theme, slug string) (Theme, bool) {
	for _, t := range themes {
		if t.Slug == slug {
			return t, true
		}
	}
	return Theme{}, false
}

// Default is the built-in theme. It is the palette every fail-soft path resolves
// to, so it always exists.
func Default() Theme {
	t, ok := Lookup(DefaultSlug)
	if !ok {
		panic("theme: the default theme is not bundled")
	}
	return t
}
