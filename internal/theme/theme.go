// Package theme is the unified theme format v1: one JSON document that paints
// both trau surfaces. A theme names twelve semantic roles per mode; the web
// resolver expands them into the CSS custom properties the SPA renders from,
// and the terminal UI reads the same roles (optionally re-pointed by a per-mode
// tui block, so a preset can keep its exact terminal hexes).
//
// The format is the interchange contract published themes are written against,
// so it is deliberately closed: unknown keys, unknown roles, unknown web
// variables and anything that is not a color literal are all rejected, and every
// error names the field it came from. Schema, derivation table and mode fallback
// are documented in docs/themes.md; the decision behind one format for two
// surfaces is ADR 0035.
package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Mode names. A theme defines at least one; a surface asked for a mode the theme
// does not define falls back to trau's built-in palette rather than to the
// theme's other mode, so a dark-only theme never paints a light UI dark.
const (
	ModeLight = "light"
	ModeDark  = "dark"
)

// ModeNames are the modes a theme may define, in the order surfaces report them.
var ModeNames = []string{ModeLight, ModeDark}

// Roles are the twelve semantic color roles every theme mode must define. They
// are also the roles a THEME_<ROLE> config key overrides, which is why
// config.ThemeRoles is this list.
var Roles = []string{
	"brand", "accent", "success", "error", "warning", "info",
	"text", "subtle", "faint", "surface", "border", "ink",
}

// Vars are the web CSS custom properties a theme resolves to — the shadcn roles
// plus trau's own accents, exactly the set web/src/styles.css defines. A theme's
// optional vars block may only name these.
var Vars = []string{
	"--background", "--foreground",
	"--card", "--card-foreground",
	"--popover", "--popover-foreground",
	"--primary", "--primary-foreground",
	"--secondary", "--secondary-foreground",
	"--muted", "--muted-foreground",
	"--accent", "--accent-foreground",
	"--destructive",
	"--border", "--input", "--ring",
	"--brand", "--teal", "--done", "--fail", "--warn", "--info", "--faint", "--cli",
}

// Theme is a theme document. Origin is not part of the format: the hub stamps it
// on the wire so a surface can tell a bundled theme from a locally saved one.
type Theme struct {
	Name    string          `json:"name"`
	Slug    string          `json:"slug"`
	Author  string          `json:"author,omitempty"`
	Version string          `json:"version,omitempty"`
	Modes   map[string]Mode `json:"modes"`
}

// Mode is one polarity of a theme. Roles carries all twelve semantic colors and
// drives both surfaces. Vars pins web variables the derivation would otherwise
// compute — the escape hatch a hand-tuned palette needs. TUI re-points roles for
// terminal rendering only, which is how a preset keeps the exact hexes its
// terminal palette shipped with.
type Mode struct {
	Roles map[string]string `json:"roles"`
	Vars  map[string]string `json:"vars,omitempty"`
	TUI   map[string]string `json:"tui,omitempty"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Parse reads and validates a theme document. It rejects unknown keys at every
// level, so a typo in a published theme fails at load instead of silently
// painting a default color.
func Parse(data []byte) (Theme, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var t Theme
	if err := dec.Decode(&t); err != nil {
		return Theme{}, fmt.Errorf("theme: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Theme{}, errors.New("theme: trailing data after the theme object")
	}
	if err := t.Validate(); err != nil {
		return Theme{}, err
	}
	return t, nil
}

// Validate reports the first field that breaks the format contract. Every
// message leads with the field's path so an author can find it.
func (t Theme) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("theme: name: must not be empty")
	}
	if !slugPattern.MatchString(t.Slug) {
		return fmt.Errorf("theme: slug: %q must match [a-z0-9][a-z0-9-]*", t.Slug)
	}
	if t.Author != "" && strings.TrimSpace(t.Author) == "" {
		return errors.New("theme: author: must not be blank")
	}
	if t.Version != "" && strings.TrimSpace(t.Version) == "" {
		return errors.New("theme: version: must not be blank")
	}
	if len(t.Modes) == 0 {
		return fmt.Errorf("theme: modes: at least one of %s is required", strings.Join(ModeNames, ", "))
	}
	for _, name := range sortedKeys(t.Modes) {
		if name != ModeLight && name != ModeDark {
			return fmt.Errorf("theme: modes.%s: unknown mode (valid: %s)", name, strings.Join(ModeNames, ", "))
		}
		if err := t.Modes[name].validate("modes." + name); err != nil {
			return err
		}
	}
	return nil
}

func (m Mode) validate(path string) error {
	if len(m.Roles) == 0 {
		return fmt.Errorf("theme: %s.roles: required (all twelve roles)", path)
	}
	for _, role := range sortedKeys(m.Roles) {
		if !isRole(role) {
			return fmt.Errorf("theme: %s.roles.%s: unknown role (valid: %s)", path, role, strings.Join(Roles, ", "))
		}
	}
	for _, role := range Roles {
		value, ok := m.Roles[role]
		if !ok {
			return fmt.Errorf("theme: %s.roles.%s: missing (all twelve roles are required)", path, role)
		}
		if _, err := ParseColor(value); err != nil {
			return fmt.Errorf("theme: %s.roles.%s: %w", path, role, err)
		}
	}
	for _, name := range sortedKeys(m.Vars) {
		if !isVar(name) {
			return fmt.Errorf("theme: %s.vars.%s: unknown web variable (valid: %s)", path, name, strings.Join(Vars, ", "))
		}
		if _, err := ParseColor(m.Vars[name]); err != nil {
			return fmt.Errorf("theme: %s.vars.%s: %w", path, name, err)
		}
	}
	for _, role := range sortedKeys(m.TUI) {
		if !isRole(role) {
			return fmt.Errorf("theme: %s.tui.%s: unknown role (valid: %s)", path, role, strings.Join(Roles, ", "))
		}
		if _, err := ParseColor(m.TUI[role]); err != nil {
			return fmt.Errorf("theme: %s.tui.%s: %w", path, role, err)
		}
	}
	return nil
}

// DefinedModes lists the modes this theme defines, in ModeNames order.
func (t Theme) DefinedModes() []string {
	out := make([]string, 0, len(t.Modes))
	for _, name := range ModeNames {
		if _, ok := t.Modes[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// ModeName maps a background polarity onto a mode name.
func ModeName(dark bool) string {
	if dark {
		return ModeDark
	}
	return ModeLight
}

func isRole(name string) bool {
	for _, role := range Roles {
		if role == name {
			return true
		}
	}
	return false
}

func isVar(name string) bool {
	for _, v := range Vars {
		if v == name {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
