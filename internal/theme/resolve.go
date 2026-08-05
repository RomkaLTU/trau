package theme

// ResolveWeb expands a mode into the full web variable set. overrides are
// THEME_<ROLE> config values; they replace roles before derivation runs, so
// everything a role feeds moves with it. A mode's explicit vars are applied last
// and win over derivation. ok is false when the theme does not define the mode —
// the caller then applies nothing and the built-in palette shows.
func (t Theme) ResolveWeb(mode string, overrides map[string]string) (map[string]string, bool) {
	m, ok := t.Modes[mode]
	if !ok {
		return nil, false
	}
	roles := resolveRoles(m.Roles, nil, overrides)
	out := derive(roles, mode == ModeDark)
	for name, value := range m.Vars {
		if c, err := ParseColor(value); err == nil {
			out[name] = c.Hex()
		}
	}
	return out, true
}

// ResolveTUI expands a mode into the twelve role hexes the terminal draws from.
// The mode's tui block re-points roles for the terminal only; overrides then
// apply on top, exactly as they do for the web.
func (t Theme) ResolveTUI(mode string, overrides map[string]string) (map[string]string, bool) {
	m, ok := t.Modes[mode]
	if !ok {
		return nil, false
	}
	roles := resolveRoles(m.Roles, m.TUI, overrides)
	out := make(map[string]string, len(Roles))
	for _, role := range Roles {
		out[role] = roles[role].Hex()
	}
	return out, true
}

// resolveRoles layers a mode's roles, its surface-specific re-points, and the
// config overrides into one parsed palette. A value that does not parse is
// dropped rather than fatal: theme files are validated at parse time, and an
// unusable config override is reported by the caller that collected it.
func resolveRoles(base, repoint, overrides map[string]string) map[string]Color {
	out := make(map[string]Color, len(Roles))
	for _, role := range Roles {
		if c, err := ParseColor(base[role]); err == nil {
			out[role] = c
		}
	}
	for role, value := range repoint {
		if c, err := ParseColor(value); err == nil {
			out[role] = c
		}
	}
	for role, value := range overrides {
		if !isRole(role) {
			continue
		}
		if c, ok := ParseOverride(value); ok {
			out[role] = c
		}
	}
	return out
}

// derive expands the twelve roles into the web variable set. The mapping is a
// published part of the format (docs/themes.md) — changing it is a format
// change. Elevation is the one polarity-aware step: a light card sits on the ink
// base untouched, a dark one steps toward the raised surface color.
func derive(r map[string]Color, dark bool) map[string]string {
	card, popover := r["ink"], r["ink"]
	if dark {
		card = mix(r["ink"], r["surface"], 0.50)
		popover = mix(r["ink"], r["surface"], 0.75)
	}
	derived := map[string]Color{
		"--background":           r["ink"],
		"--foreground":           r["text"],
		"--card":                 card,
		"--card-foreground":      r["text"],
		"--popover":              popover,
		"--popover-foreground":   r["text"],
		"--primary":              r["brand"],
		"--primary-foreground":   r["ink"],
		"--secondary":            r["surface"],
		"--secondary-foreground": r["text"],
		"--muted":                r["surface"],
		"--muted-foreground":     r["subtle"],
		"--accent":               r["surface"],
		"--accent-foreground":    r["text"],
		"--destructive":          r["error"],
		"--border":               r["border"],
		"--input":                mix(r["surface"], r["border"], 0.50),
		"--ring":                 r["brand"],
		"--brand":                r["brand"],
		"--teal":                 r["accent"],
		"--done":                 r["success"],
		"--fail":                 r["error"],
		"--warn":                 r["warning"],
		"--info":                 r["info"],
		"--faint":                r["faint"],
		"--cli":                  r["brand"],
	}
	out := make(map[string]string, len(derived))
	for name, c := range derived {
		out[name] = c.Hex()
	}
	return out
}
