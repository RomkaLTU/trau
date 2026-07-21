# ADR 0019 — Per-workspace config scoping via nested `.trau.ini` overlays

- **Status:** Accepted
- **Date:** 2026-07-21
- **Deciders:** Romas (sole maintainer)

## Context

One `<repo>/.trau.ini` configures the whole repo (ADR 0016). In a monorepo,
workspaces can legitimately need different values — `LINT_FIX_CMD`,
`APP_URL`, `AGENT_TIMEOUT`, verify checks — and there was no way to scope any
knob below the git root.

COD-973 already solved this once, bespoke, for a single knob: `APP_URLS`
reads a comma-separated `<workspace>=<url>` map from the repo-root file and
`agent.WorkspaceAppURL` picks the entry for the workspace holding the slice's
changed files. That shape doesn't generalize — every newly-scoped knob would
need its own map-typed key, its own parser, and its own catalog entry.

Two shapes were on the table:

1. **Sectioned keys in the root `.trau.ini`** — `[workspace apps/web]` blocks
   or `WORKSPACE_<path>_<KEY>` names. One file, explicit precedence, but a
   syntax the flat `KEY=value` parser doesn't have, and every scoped key still
   needs bespoke encoding (as `APP_URLS` already shows).
2. **Nested `<workspace>/.trau.ini` overlays**, merged on top of the root file
   when a slice's changed files live in that workspace. Reuses the existing
   `KEY=value` format and `ParseEnvFile` parser verbatim — a workspace's own
   file is just a `.trau.ini` that happens to sit in its own directory.

## Decision

Shape 2. A workspace's own `.trau.ini` overrides individual keys for slices
whose changed files live in that workspace, checked after the slice's diff is
known, before falling back to the repo-root value. Two primitives back it:

- `agent.OwningWorkspaceDir(repoRoot, changed)` finds the workspace holding
  the plurality of a slice's changed files — the same counting and
  tie-breaking `WorkspaceAppURL` already does for `APP_URLS` — returning `""`
  when nothing matches or two workspaces tie.
- `config.WorkspaceOverride(workspaceDir, key)` reads
  `<workspaceDir>/.trau.ini` with the existing `ParseEnvFile` and returns the
  key's value if the file sets it.

A call site that resolves a knob once the diff is known composes the two:
`sliceLintFixCmd` mirrors the existing `sliceAppURL` — resolve the owning
workspace, look up the key there, fall back to the already-loaded repo-root
config value on any miss (no override file, no match, unreadable tree). This
does not become a fifth layer in `LoadLayeredWithSources`: the pipeline still
loads one `Config` at startup, and workspace overrides are resolved lazily,
per-slice, only for the knobs a call site opts into.

`APP_URLS` is untouched by this decision — folding it into a workspace's
plain `APP_URL` key is a possible follow-up now that the general mechanism
exists, but it is separate, non-blocking cleanup.

`AGENT_TIMEOUT` (baked into the agent `Runner` before any repo or slice is
known) and `.trau/checks` verify checks (loaded once at pipeline
construction) stay repo-root-only for now: scoping either needs re-plumbing
*when* the value is read, not just adding a lookup at the point of use that
already runs after the diff is known.

## Consequences

- Any knob a call site chooses to resolve this way becomes workspace-scopable
  with zero catalog or parser changes — a workspace's `.trau.ini` is exactly
  the same file format as the repo root's.
- The pipeline's single-working-tree model is unaffected: workspace overrides
  change *values* a phase reads, never the working directory a phase executes
  in (`c.Dir` stays `RepoRoot`).
- A workspace `.trau.ini` is invisible to the config-source UI (ADR 0011) — it
  is a plain file a repo maintainer commits, not a catalog-tracked layer, so
  its precedence doesn't show up in `trau doctor` or the settings surface.
  Worth a follow-up if workspace overrides see real use.
- Wiring stays opt-in per knob; each one a monorepo needs gets its own
  one-line resolver next to `sliceAppURL`/`sliceLintFixCmd`, not a blanket
  mechanism that intercepts every config read.
