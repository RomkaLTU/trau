package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/registry"
)

// OriginLocal marks a theme saved on this machine, as opposed to one compiled
// into the binary.
const OriginLocal = "local"

// MaxFileBytes caps one saved theme file. A theme is twelve colors per mode plus
// two small override blocks, so anything larger is not a theme.
const MaxFileBytes = 64 << 10

// LocalDirName is the directory saved themes live in under a trau home. Themes
// are configuration, so they stay files on disk rather than rows in the hub
// database: a user can drop one in by hand and the hub picks it up.
const LocalDirName = "themes"

// LocalDir returns the directory a trau home keeps saved themes in. An empty
// home resolves to an empty path, which every caller reads as "no saved themes"
// rather than as an error.
func LocalDir(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, LocalDirName)
}

// Skipped names a saved file the loader refused, and why. Loading is fail-soft:
// a theme that no longer parses must never stop the hub from booting, so the
// loader collects these for the caller to log.
type Skipped struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type localEntry struct {
	theme Theme
	path  string
	// shadowed are the other files in the directory that resolve to the same
	// slug. Loading ignores them, but a save or a delete has to take them with
	// it: left behind, one of them becomes the theme the next load reads.
	shadowed []string
}

// files lists every file the slug is held by, the one that wins first.
func (e localEntry) files() []string {
	return append([]string{e.path}, e.shadowed...)
}

// LoadLocal reads the themes saved in dir, in slug order, alongside the files it
// refused. A missing directory is an empty set: the directory is created on the
// first save, and a trau home without one has no saved themes.
func LoadLocal(dir string) ([]Theme, []Skipped) {
	entries, skipped := scanLocal(dir)
	out := make([]Theme, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.theme)
	}
	return out, skipped
}

// Local reads the themes saved under this machine's trau home. The hub reads its
// own configured home instead, so a test hub never sees the developer's themes.
func Local() ([]Theme, []Skipped) {
	return LoadLocal(LocalDir(registry.Home()))
}

func scanLocal(dir string) ([]localEntry, []Skipped) {
	entries := []localEntry{}
	skipped := []Skipped{}
	if dir == "" {
		return entries, skipped
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			skipped = append(skipped, Skipped{File: dir, Reason: err.Error()})
		}
		return entries, skipped
	}
	// Two files can claim one slug — a save writes <slug>.json while a
	// hand-dropped file may be called anything. The canonical name wins so the
	// theme the editor last wrote is the one that shows.
	bySlug := map[string]localEntry{}
	for _, file := range files {
		if file.IsDir() || !strings.EqualFold(filepath.Ext(file.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, file.Name())
		t, reason := readLocalFile(path)
		if reason != "" {
			skipped = append(skipped, Skipped{File: path, Reason: reason})
			continue
		}
		if _, reserved := LookupBundled(t.Slug); reserved {
			skipped = append(skipped, Skipped{File: path, Reason: fmt.Sprintf("slug %q is a predefined theme name", t.Slug)})
			continue
		}
		candidate := localEntry{theme: t, path: path}
		held, dup := bySlug[t.Slug]
		if !dup {
			bySlug[t.Slug] = candidate
			continue
		}
		winner, loser := held, candidate
		if isCanonicalName(candidate) && !isCanonicalName(held) {
			winner, loser = candidate, held
		}
		winner.shadowed = append(winner.shadowed, loser.files()...)
		bySlug[t.Slug] = winner
		skipped = append(skipped, Skipped{
			File:   loser.path,
			Reason: fmt.Sprintf("slug %q is already saved as %s", t.Slug, filepath.Base(winner.path)),
		})
	}
	for _, e := range bySlug {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].theme.Slug < entries[j].theme.Slug })
	return entries, skipped
}

func isCanonicalName(e localEntry) bool {
	return filepath.Base(e.path) == e.theme.Slug+".json"
}

func readLocalFile(path string) (Theme, string) {
	info, err := os.Stat(path)
	if err != nil {
		return Theme{}, err.Error()
	}
	if info.Size() > MaxFileBytes {
		return Theme{}, fmt.Sprintf("file is %d bytes, over the %d byte cap", info.Size(), MaxFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Theme{}, err.Error()
	}
	t, err := Parse(data)
	if err != nil {
		return Theme{}, err.Error()
	}
	return t, ""
}

// SaveLocal writes t into dir in the canonical form and reports whether it
// replaced a theme already saved under the same slug. The slug names the file,
// so any other file holding that slug — one dropped in by hand, one the loader
// was already skipping — is removed rather than left behind as a duplicate.
func SaveLocal(dir string, t Theme) (bool, error) {
	if dir == "" {
		return false, errors.New("theme: no trau home to save themes in")
	}
	if err := t.Validate(); err != nil {
		return false, err
	}
	if _, reserved := LookupBundled(t.Slug); reserved {
		return false, fmt.Errorf("theme: slug: %q is a predefined theme name", t.Slug)
	}
	data, err := Canonical(t)
	if err != nil {
		return false, err
	}
	if len(data) > MaxFileBytes {
		return false, fmt.Errorf("theme: is %d bytes, over the %d byte cap", len(data), MaxFileBytes)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("theme: create %s: %w", dir, err)
	}
	entries, _ := scanLocal(dir)
	path := filepath.Join(dir, t.Slug+".json")
	replaced := false
	for _, e := range entries {
		if e.theme.Slug != t.Slug {
			continue
		}
		replaced = true
		if err := removeFiles(e.files(), path); err != nil {
			return false, err
		}
	}
	if err := writeFileAtomic(path, data); err != nil {
		return false, err
	}
	return replaced, nil
}

// DeleteLocal removes the saved theme named by slug and reports whether there
// was one. Every file holding the slug goes, not just the one that was loaded,
// or the theme would come back on the next load from a file the user never saw.
// Predefined themes are not saved files and can never be deleted, so they read
// as absent here and the caller refuses them by name.
func DeleteLocal(dir, slug string) (bool, error) {
	entries, _ := scanLocal(dir)
	slug = Normalize(slug)
	for _, e := range entries {
		if e.theme.Slug != slug {
			continue
		}
		if err := removeFiles(e.files(), ""); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// removeFiles deletes every path but keep, which a save passes to spare the file
// it is about to write over.
func removeFiles(paths []string, keep string) error {
	for _, path := range paths {
		if path == keep {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("theme: remove %s: %w", path, err)
		}
	}
	return nil
}

// Canonical renders t as the theme file a user downloads and later uploads: the
// format's own fields, two-space indent, trailing newline.
func Canonical(t Theme) ([]byte, error) {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("theme: render %q: %w", t.Slug, err)
	}
	return append(data, '\n'), nil
}

// writeFileAtomic renames a fully written temp file into place so a reader — the
// hub's next load, a terminal starting up — never sees half a theme.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".theme-*.json")
	if err != nil {
		return fmt.Errorf("theme: create temp file in %s: %w", filepath.Dir(path), err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("theme: write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("theme: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("theme: install %s: %w", path, err)
	}
	return nil
}
