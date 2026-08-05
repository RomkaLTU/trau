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

## Writing a theme

### The editor

Settings → Appearance is the easy path. **Create theme** opens the active theme
as a draft; **Duplicate & edit** on any card opens that one. Each mode gets the
twelve roles as a swatch and a hex field, and the whole app previews the draft
live — buttons, focus rings, cards, every screen — while the editor is open. The
preview is browser state only: nothing is written to config or to disk until you
save, and **Cancel** puts the active theme back at once. A half-typed hex is
reported inline and the preview keeps the last color that parsed, so the app
never goes dark mid-edit.

The name derives the slug (`My Theme` → `my-theme.json`). Predefined slugs are
reserved; a slug already saved asks before it replaces that theme. A mode can be
added or removed, and at least one is required. **Save theme** installs it and
offers to activate it, which is an ordinary `THEME=<slug>` write to the layer the
write-target control names.

`vars` and `tui` blocks a duplicated theme pinned ride along untouched. v1 has no
editing surface for them — the roles are the editor's whole vocabulary — but they
survive the round trip, so duplicating a bundled preset never loses its exact
terminal hexes.

### Exporting and sharing

**Copy JSON** and **Download .json** produce the canonical theme file: metadata,
then each mode's roles and any pinned `vars`/`tui`. That file is the shareable
theme — the same document trau.sh will take when community submissions open — and
it re-resolves identically to the theme it came from, which is a tested property
of the format rather than a convention.

### Saved themes on disk

Saved themes are files at `<trau home>/themes/<slug>.json`, where the trau home is
`$TRAU_HOME` or `~/.trau` — the directory the hub's own data lives in. Themes are
configuration, so they legitimately stay on disk rather than in the hub database.

The hub reads the directory whenever it serves themes, so **dropping a valid file
in by hand works too**: it joins `GET /api/v1/themes` with `origin: "local"`, the
picker, and the `THEME` catalog. Loading is fail-soft — a file that is unreadable,
over the 64 KiB cap, unparseable, or claiming a predefined slug is skipped with a
logged note and every sibling still loads. Nothing in the UI promises to explain a
hand-dropped file that was refused; run the hub with `--verbose` to see the note.

Deleting a saved theme from the picker removes the file. A `THEME` value naming a
theme that is no longer installed falls back to `default` with a note, exactly as
an unknown name always has.

## The hub's themes resource

`GET /api/v1/themes` lists the themes this machine carries — bundled and saved —
and resolves the active one:

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

`origin` is `bundled` for a theme compiled into the binary and `local` for one
saved on this machine. `note` appears when `THEME` names a theme that is no
longer installed and the default took over.

The SPA applies the resolved palettes as a stylesheet layered over the
`styles.css` defaults, caches the last one, and replays it before first paint, so
a themed UI does not flash the built-in colors on reload.

Four more endpoints back the editor. All of them sit behind the hub's existing
bearer-token gating and nothing else: saving a theme is a cosmetic write that
widens no capability, so unlike repo registration it takes no second gate.

| Endpoint | What it does |
| --- | --- |
| `POST /api/v1/themes` | Installs a theme file. Full format validation plus a 64 KiB cap; a predefined slug is refused as a reserved name (409); an already-saved slug is overwritten and the response says `"replaced": true`. Nothing is stored when validation fails. |
| `GET /api/v1/themes/{slug}` | The theme as its canonical file — the export, and the source a duplicate copies. |
| `DELETE /api/v1/themes/{slug}` | Removes a saved theme. Predefined themes refuse (409); an unknown slug is a 404. Deleting the active theme leaves `THEME` alone and the default paints instead. |
| `POST /api/v1/themes/resolve` | Resolves a draft document into its per-mode variable sets without storing anything. The editor calls it debounced for the live preview. |

`resolve` deliberately ignores `THEME_<ROLE>` overrides: they are this repo's
resolution of a theme, not part of the theme, and a draft has to preview the
colors its author is writing. It is the same rule the picker's swatches follow.
