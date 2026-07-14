# ADR 0011 — Settings surface is catalog-driven; Web-editable is a fail-closed per-key flag

- **Status:** Accepted
- **Date:** 2026-07-14
- **Deciders:** Romas (sole maintainer)

## Context

The settings page is being rebuilt around grouped Sections, a per-phase
routing matrix, and inline editing of far more keys than the web may touch
today. Editability currently lives as a 20-key allowlist inside the web
handler (`safeEditKeys`), grouping metadata exists nowhere, model and effort
keys reach the browser as free text even though `ProviderTuningMetas` was
built to feed pickers, and value validation covers only bools and enum
options. Meanwhile the key catalog (`KnownKeys()`) already drives both the
TUI and web settings rendering, and it churns weekly with new keys — any
metadata kept outside it drifts.

## Decision

**The key catalog owns settings-surface metadata.** `KeyMeta` grows a Section
(group), a value kind (`int`, `color` — joining the existing bool/options),
web-editability, and picker suggestions — declared where the key is defined,
delivered per key over the config API. Web and TUI render the same Sections;
a test holds every key to having one. Clients keep pure presentation only:
section order and descriptions, the routing-matrix and theme-grid renderers.

**Web-editability is fail-closed.** A key is read-only over the web unless
flagged; a newly added key defaults to read-only. The flag is on for
operational knobs — pipeline, CI, verification, cost caps, skills, grilling,
agent runtime, models/efforts, the per-phase routing family, `THEME_*` roles,
notifications, timelog, tracker identity, hub tuning and retention. It stays
off permanently for three classes: exec-adjacent keys (`*_BIN`, `*_FLAGS`,
`*_CONFIG`, `LINT_FIX_CMD` — a web write there is arbitrary command
execution), exposure keys (`SERVE_BIND`, `SERVE_PORT`, `SERVE_TOKEN`,
`SERVE_ALLOW_REGISTER` — lockout or attack-surface changes), and paths
(`RUNS_DIR`, `TRAU_REPO_ROOT`, `SERVE_WORKSPACE` — a typo silently relocates
run data).

**Secrets are write-only or nothing.** `LINEAR_API_KEY` and `JIRA_API_TOKEN`
can be set and rotated from the web but are never sent back. `SERVE_TOKEN`
cannot be written at all — it guards the very surface doing the writing.

**Unset is a first-class write.** Reset-to-default deletes the key's line
from the Write target's file, restoring inheritance. Saving an empty string
keeps meaning "explicitly empty" — the two operations stay distinct.

**Suggestions and options are different things on the wire.** Effort keys
validate strictly against each provider's real effort set. Model keys carry
catalog models as suggestions only — a custom id is always writable, because
new model ids ship faster than the catalog.

## Considered options

- **Keep the endpoint allowlist** — already lies against the new UI (matrix
  and swatch editors would 403), and every expansion is a handler edit far
  from the key's definition.
- **Client-side grouping map** (what the prototype hardcodes) — an unmapped
  new key silently renders nowhere; with the catalog churning weekly that is
  a standing drift bug, and the TUI could never share it.
- **Denylist posture** ("everything editable except…") — fail-open for new
  keys, the wrong default for a surface that can hold credentials and
  exec-adjacent keys (ADR 0003 set the fail-closed precedent).

## Consequences

- `safeEditKeys` dies; the write handler consults the catalog. Shipping a new
  key now means declaring section, kind, and editability once, in one place.
- Writes gain kind validation (`int`, `color`); shadow warnings (env var over
  everything writable, user over project) stay client-side — layer and target
  are already on the wire.
- The TUI settings editor regroups by the same Section metadata in the same
  iteration — one vocabulary across surfaces.
- Glossary gains **Config layer**, **Write target**, **Web-editable**,
  **Shadowed**, and **Section**.
