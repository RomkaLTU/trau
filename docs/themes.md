# Theme format v1

A trau theme is one JSON document that paints both surfaces: the terminal UI's
semantic palette and the web UI's CSS variables. `THEME=<slug>` selects it, and
both surfaces resolve the same document — there is no second palette to keep in
step.

Format v1 is **stable**. It is the interchange contract themes published to
trau.sh are written against: a v1 document keeps resolving the same way in later
trau releases, and the derivation table below is part of that promise.

The decision behind one format for two surfaces is [ADR 0035](adr/0035-unified-theme-format.md).
The reader, validator and resolver live in `internal/theme`.

## Schema

```json
{
  "name": "Nord",
  "slug": "nord",
  "author": "Arctic Ice Studio",
  "version": "1",
  "modes": {
    "dark": {
      "roles": { "brand": "#88c0d0", "...": "..." },
      "vars": { "--cli": "#b48ead" },
      "tui": { "surface": "#3f4551" }
    }
  }
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Human-readable title. |
| `slug` | yes | The `THEME` value. Must match `[a-z0-9][a-z0-9-]*`. |
| `author` | no | Credit for the palette. |
| `version` | no | The theme's own version, not the format's. |
| `modes` | yes | At least one of `light`, `dark`. |
| `modes.<mode>.roles` | yes | All twelve semantic roles. |
| `modes.<mode>.vars` | no | Web variables pinned explicitly. |
| `modes.<mode>.tui` | no | Roles re-pointed for terminal rendering only. |

Validation is closed and every message names the field it came from:

- An unknown key anywhere — top level, mode, `roles`, `vars`, `tui` — is
  rejected, so a typo fails at load instead of silently painting a default.
- `roles` must carry exactly the twelve roles: no fewer, no extras.
- Every color is a literal `#rgb`, `#rrggbb` or `#rrggbbaa`. Names, `rgb()`,
  `var()` and bare hex without the hash are all refused — a resolved value
  reaches the browser as raw CSS, so nothing but a color may pass.
- `vars` may only name the variables in the derivation table.

## The twelve roles

| Role | What it means |
| --- | --- |
| `brand` | The product accent: primary buttons, focus rings, links. |
| `accent` | The secondary accent. |
| `success` | A finished, passing, merged thing. |
| `error` | A failed thing. |
| `warning` | A thing that needs attention but has not failed. |
| `info` | Neutral emphasis. |
| `text` | Body text on the base background. |
| `subtle` | De-emphasized text that must still read. |
| `faint` | Text and rules at the edge of legibility: timestamps, hints. |
| `surface` | The raised fill: panels, chips, inputs, hover rows. |
| `border` | Hairlines between regions. |
| `ink` | Text drawn on top of a chromatic fill — and, on the web, the base the UI is printed on. |

`THEME_<ROLE>` config keys override roles by the same names
(`THEME_BRAND=#ff0000`). An override replaces the role **before** anything is
derived from it, on both surfaces, so everything the role feeds moves with it.

## Web derivation

The web UI needs twenty-six variables; a theme names twelve roles. The rest
derive, deterministically, from this table. `mix(a, b, t)` blends `a` toward `b`
by `t` in sRGB byte space, rounding half away from zero.

| Variable | Derived from |
| --- | --- |
| `--background` | `ink` |
| `--foreground` | `text` |
| `--card` | light: `ink` · dark: `mix(ink, surface, 0.50)` |
| `--card-foreground` | `text` |
| `--popover` | light: `ink` · dark: `mix(ink, surface, 0.75)` |
| `--popover-foreground` | `text` |
| `--primary` | `brand` |
| `--primary-foreground` | `ink` |
| `--secondary` | `surface` |
| `--secondary-foreground` | `text` |
| `--muted` | `surface` |
| `--muted-foreground` | `subtle` |
| `--accent` | `surface` |
| `--accent-foreground` | `text` |
| `--destructive` | `error` |
| `--border` | `border` |
| `--input` | `mix(surface, border, 0.50)` |
| `--ring` | `brand` |
| `--brand` | `brand` |
| `--teal` | `accent` |
| `--done` | `success` |
| `--fail` | `error` |
| `--warn` | `warning` |
| `--info` | `info` |
| `--faint` | `faint` |
| `--cli` | `brand` |

Three variables are polarity-aware because elevation is: a card in a light UI
sits on the ink base untouched, a card in a dark UI steps toward the raised
surface color.

A mode's `vars` block pins variables the derivation would otherwise compute, and
**wins over derivation**. It is the escape hatch a hand-tuned palette needs — the
bundled `default` theme uses it for the few warm neutrals its derivation cannot
reproduce, which is why `THEME=default` resolves byte-for-byte to the palette
`web/src/styles.css` ships. Because a pin wins, a variable a theme pins does not
move when the role behind it is overridden.

## Terminal rendering

The terminal draws the twelve roles directly. A mode's optional `tui` block
re-points roles for the terminal only, which is how a bundled theme keeps the
exact hexes its terminal palette has always rendered with while its web roles
are authored for a browser. Overrides apply on top of the `tui` block, exactly as
they do on the web.

## Mode fallback

A theme defines one or both modes. A surface asked for a mode the theme does not
define **falls back to trau's built-in palette**, never to the theme's other
mode: a dark-only theme leaves a light UI light. On the web that means no
variables are applied for that mode and the `styles.css` defaults show through;
in the terminal it means the built-in palette for that background polarity.

An unknown `THEME` value falls back to `default` with a note, and never fails a
run.

## The hub's themes resource

`GET /api/v1/themes` lists the themes this build carries and resolves the active
one:

```json
{
  "active": "nord",
  "themes": [
    { "slug": "nord", "name": "Nord", "author": "Arctic Ice Studio",
      "version": "1", "modes": ["light", "dark"], "origin": "bundled" }
  ],
  "resolved": { "light": { "--background": "#eceff4" }, "dark": { "…": "…" } }
}
```

`resolved` carries the full variable set per mode the active theme defines, with
`THEME_<ROLE>` overrides already applied — the SPA applies colors without knowing
the derivation rules. An optional `?repo=<name-or-root>` reads `THEME` from that
registered project's config; without it only the machine baseline
(`~/.trau.ini`) and the environment decide.

The SPA applies the resolved palettes as a stylesheet layered over the
`styles.css` defaults, caches the last one, and replays it before first paint, so
a themed UI does not flash the built-in colors on reload.
