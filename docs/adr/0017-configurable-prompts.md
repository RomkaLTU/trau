# ADR 0017 — Configurable prompts

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** Romas (sole maintainer)

## Context

Every phase prompt lived as a Go concatenation function scattered across
`internal/pipeline` (build, handoff, verify, commit, repair, bugfix,
push-repair, resolve-conflicts, epic-repair, cleanup, lint-fix,
lessons-distill, rubric, build-notes, timelog-estimate) plus shared fragments
in `internal/config` (the unattended preambles) and `pipeline` (the
code-style note, the skills sentence). The wording was unreachable without a
rebuild, invisible to the web UI, and impossible to override per repo — yet
the prompts are the loop's real behavior surface, and tuning them per project
is a legitimate need.

Prompts also carry hard contracts the loop parses back: verdict and rubric
JSON shapes, handoff/notes file paths, the REFUSED sentinel. Any override
mechanism must not let a reworded prompt silently break those.

## Decision

Prompts are named `text/template`s in a registry (`internal/prompts`), one
stable snake_case name per prompt, each with a typed data struct carrying raw
values (ids, branches, file paths, schemas, pre-rendered fragments). Prose
variants live inside the templates as `{{if}}` blocks so an override can
reword them; Go keeps only genuinely computed values. The registry holds
per-prompt metadata — title, description, placeholder list with a required
flag, and the built-in default body — surfaced via `prompts.Catalog()`;
`prompts.Render(name, data)` renders. The package is a leaf so `config`,
`pipeline`, and `webserver` can all import it.

Overrides live in the hub DB keyed `(name, repo)`, resolved
repo > global > built-in. Validation is fail-closed: an override that does
not parse, or that drops a required placeholder (the fields carrying parsing
and file-path contracts), is rejected and the built-in default is used.
Resolution snapshots at ticket-run start, so a mid-run edit never splits one
run across two prompt versions.

Deliberately excluded from the registry: tracker MCP prompt fragments and
the path-pointer note helpers (rubric/build-notes/lessons pointer notes) —
they are mechanical glue around loop-owned artifacts, not wording anyone
should retune.

## Consequences

- One source of truth: the prompt inventory, its placeholders, and defaults
  are enumerable, which the settings UI and override API build on directly.
- Behavior is pinned: golden tests assert byte-identical rendering against
  the pre-refactor strings, including every prose variant branch.
- Required placeholders make the pipeline contracts explicit per prompt
  instead of implicit in concatenation order.
- Override plumbing (config keys, DB, API, UI) lands in later slices; until
  then `Render` always uses the built-in defaults.
