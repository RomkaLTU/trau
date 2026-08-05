# ADR 0035 — One theme format paints both surfaces

- **Status:** Accepted
- **Date:** 2026-08-05
- **Deciders:** Romas (sole maintainer)

## Context

trau has two front ends and, until now, two unrelated palettes. The terminal UI
mapped a `THEME` preset name onto a bubbletint variant and blended a twelve-role
semantic palette out of it. The web UI hardcoded twenty-six CSS variables in
`web/src/styles.css`, and the only choice a user had there was light, dark or
system. `THEME=nord` recolored the terminal and did nothing at all to the
browser.

Keeping them apart was cheap while the web UI was small. It is not cheap now: the
hub is where most of the product is looked at, a theme that stops at the terminal
reads as a half-built feature, and the picker and editor slices that follow this
one would otherwise have to ship twice — once per surface, against two different
notions of what a theme is.

There is a second, larger pull. Themes are meant to be shared: authored by hand,
uploaded to trau.sh, installed by slug. That only works if a theme is a document
with a published schema rather than a shape that happens to fall out of whatever
the two renderers currently read.

## Decision

**A theme is one JSON document, and it drives both surfaces.** The format,
reader, validator and resolver live in `internal/theme`; `docs/themes.md` is its
published specification. `THEME=<slug>` selects the same document for the
terminal palette and the web variables.

The format is **v1 and stable**, because it is the interchange contract uploads
are written against. That makes the shape of the document the decision:

- **Twelve semantic roles per mode, not twenty-six variables.** The roles are the
  ones `THEME_<ROLE>` already overrides, so a config override and a theme file
  name the same things. The web's remaining variables *derive* from the roles
  through a table published alongside the schema. A theme author picks twelve
  colors, not twenty-six, and the meaning of each one is stated.
- **Explicit `vars` win over derivation.** A derivation table good enough for a
  hand-tuned palette does not exist; the escape hatch is what keeps the table
  honest for everyone else. It is also what lets `THEME=default` resolve
  byte-for-byte to the palette `styles.css` already shipped — enforced by a test
  that reads the stylesheet rather than a copy of it.
- **An optional per-mode `tui` block re-points roles for the terminal only.** The
  bundled presets carry the exact hexes their bubbletint-backed palettes rendered
  with, so adopting the format changed no terminal pixel, while their web roles
  are authored for a browser, where a palette blended for a terminal reads badly.
- **Strict, closed validation, with the field named.** Unknown keys, unknown
  roles, unknown variables and any value that is not a `#rgb` / `#rrggbb` /
  `#rrggbbaa` literal are all rejected. This is a security property, not a
  tidiness one: a resolved value is injected into a stylesheet, so nothing that
  is not a color may survive validation — on either side of the wire, since the
  SPA re-checks the palette it reads back out of its own cache.
- **A mode a theme does not define falls back to the built-in palette, never to
  the theme's other mode.** A dark-only theme leaves a light UI light.

The hub serves `GET /api/v1/themes` — the bundled set plus the active theme's
palettes already resolved into web variables, with `THEME_<ROLE>` overrides
applied. The SPA applies colors it is handed and never reimplements derivation,
so the two surfaces cannot drift.

`THEME_<ROLE>` overrides apply **before** derivation on both surfaces, so an
override moves everything the role feeds rather than one variable.

## Consequences

- The bundled themes are embedded JSON, and the `THEME` catalog options come
  from that set rather than a literal list — adding a theme file is the whole of
  adding a theme.
- `internal/config` depends on `internal/theme` for the role list and the slugs.
  The dependency runs one way: the format package knows nothing about config.
- bubbletint leaves the dependency set. Preset palettes are now data in the
  repository, which is what makes them auditable and editable.
- The web UI paints over its `styles.css` defaults rather than replacing them, so
  a hub that cannot be reached still renders the built-in palette. The last
  resolved palette is cached and replayed before first paint; a first-ever load
  of a themed hub may still show one frame of the default colors.
- The picker and the editor slices build on this format. A locally saved theme
  joins the same resource with a different `origin`; nothing about the document
  changes.
