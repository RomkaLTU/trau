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

// Slugs lists the bundled theme names in Bundled order.
func Slugs() []string {
	src := bundled()
	out := make([]string, 0, len(src))
	for _, t := range src {
		out = append(out, t.Slug)
	}
	return out
}

// Lookup resolves a configured THEME value. The name is normalized the way
// config values are read everywhere else — trimmed and lowercased — and an empty
// name means the default theme.
func Lookup(name string) (Theme, bool) {
	slug := strings.ToLower(strings.TrimSpace(name))
	if slug == "" {
		slug = DefaultSlug
	}
	for _, t := range bundled() {
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
