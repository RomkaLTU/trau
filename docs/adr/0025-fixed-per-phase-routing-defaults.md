# ADR 0025 — Fixed per-phase routing defaults replace the provider-default cascade

- **Status:** Accepted
- **Date:** 2026-07-30
- **Deciders:** Romas (sole maintainer)
- **Refs:** [COD-1333]; [ADR 0011](0011-catalog-driven-settings-surface.md) (the catalog is the one source every settings surface reads)

## Context

Phase routing resolved in two steps. A `<PROVIDER>_<PHASE>_MODEL` /
`_EFFORT` key won if it was set; otherwise the phase fell through to the
provider-level `CLAUDE_MODEL` / `CLAUDE_EFFORT` (or the Codex/Kimi pair). Four
Claude phases were seeded — cleanup, commit and handoff onto sonnet, lintfix onto
haiku — because the cascade otherwise put them on Opus, and Opus for a commit
message is a cost trap nobody opts into deliberately.

That left two problems. The cascade is invisible: every unset cell in the web
matrix and the TUI picker read "inherit", so the only way to learn what a phase
actually runs on was to reason about the layering. And it is coarse: a provider
default is one dial for nine phases whose costs differ by an order of magnitude,
so the one lever a user reaches for — downtier the default — moves the phases
that should stay expensive along with the ones that should not.

The seed table proved the fix. Four phases already had opinionated defaults that
nobody complained about; the rest were unseeded only because the cascade happened
to be there.

## Decision

### 1. Every phase has a fixed default, and the provider default steers none of them

`phaseRouteDefaults` in `internal/config/config.go` carries a model and an effort
per provider and phase:

| provider | phases | default |
| -- | -- | -- |
| claude | build, verify, repair, bugfix | opus |
| claude | pick, cleanup, commit, handoff | sonnet |
| claude | lintfix | haiku |
| codex | build, verify, repair, bugfix | gpt-5.6-sol @ medium |
| codex | pick, cleanup, commit, handoff | gpt-5.6-sol @ low |
| codex | lintfix | gpt-5.4-mini @ low |

Claude leaves effort unset so the CLI applies its own; Codex uses effort as the
cost lever because its tiers share one model. Resolution is now one rule with no
fallback chain: an explicit per-phase key, or the fixed default. Each half
resolves independently, so a phase with only an effort override keeps its default
model.

`pick` is also what the tracker's pick and epic-pick calls run under, so those
light judgments move to the pick tier with it. That is intended.

### 2. Kimi is the exception

Kimi selects models by alias from the user's own `~/.kimi-code/config.toml`, so
there is no name to pin that would mean anything on another machine. Kimi phases
keep defaulting to `KIMI_MODEL`; the UI shows the resolved alias rather than a
sentinel. `phaseRouteDefault` takes `KIMI_MODEL` as an argument for exactly this
case.

### 3. Provider default keys are demoted, not removed

`CLAUDE_MODEL` / `CLAUDE_EFFORT` and `CODEX_MODEL` / `CODEX_EFFORT` still feed the
non-phase agents — hub grilling sessions and the hub helper agent — and their
catalog descriptions now say so. They no longer reach a phase, which is also why
`parsePhaseRoute` exists alongside `parseRoute` in `cmd/trau`: a phase route is
already fully resolved by config, so an omitted effort means the CLI's default,
while a user-authored `VERIFY_PANEL` or `FALLBACK_PROVIDERS` spec keeps taking the
provider default it always did.

### 4. One table, three readers

Route building, `effectiveRoute` (the routing fingerprint), and
`ResolveProviderTunings` (the TUI panel) all call `phaseRouteDefault`, and
`KnownKeys` stamps the same values into each per-phase key's `Default` so the web
matrix and the docs derive from it too. A fingerprint that disagreed with dispatch
would describe a run that did not happen, and duplicating the table is how that
happens.

Fingerprints shift once and experiment cohorts re-bucket. There is no
compatibility shim: the hashes describe effective routing, and effective routing
genuinely changed.

### 5. A one-time migration preserves existing effective routing

Someone who set `CLAUDE_MODEL=sonnet` ran sonnet on all nine phases. Under the
fixed matrix the heavy four would jump back to Opus without them touching
anything — a cost surprise, not a preference. `MigratePhaseRoutes` walks each
provider whose default is set in a **file** layer and, for every phase whose
per-phase key is absent everywhere and whose old value differs from the new
default, pins the old value as an explicit key **in the same file**. Halves
migrate independently.

It runs at the two entrypoints that own a startup — the loop in `cmd/trau/main.go`
and `trau serve` — not from `loadServeConfig`, which `trau watch`, `trau steer`,
`trau hub` and the forensics commands also read their config through. Those are
read-only and stay that way.

Idempotency by construction is not enough, because the provider default stays in
the file: a phase the user later resets back to its default would be re-pinned on
the next startup, which is the one thing the ADR promises it will not do. So every
file the pass writes gets `PHASE_ROUTES_PINNED=1` in the same write, and a marked
file is never a migration source again. A second startup writes nothing.

That marker only covers files the pass has already expanded, and a config written
*after* the upgrade needs the same protection: someone setting `CLAUDE_MODEL` today
means it for grill sessions and the hub agent, and pinning that onto nine phases
would be the leak this ADR exists to close. So the first startup under the fixed
defaults stamps `phase-routes-upgraded` in the trau home, and only a config file
older than that stamp is a migration candidate. A run with no resolvable trau home
cannot date the upgrade and skips the pass.

A provider default supplied only through the environment has no file to be pinned
into and is skipped — that config re-tiers to the new defaults.

## Consequences

- Every settings surface renders a concrete model and effort. The word "inherit"
  is gone from phase routing; an unset cell shows the default it runs at, muted,
  and an explicit one carries its layer chip.
- Fresh installs get the seed table's economics on all nine phases instead of
  four, which is a real cost reduction on pick and a real cost increase on nothing.
- The migration writes to a user's config file on startup. That is intrusive, and
  the alternative — silently re-tiering someone's spend — is worse. It only ever
  writes keys that reproduce what was already running, plus the
  `PHASE_ROUTES_PINNED` marker that keeps it from running twice.
- A repo whose project `.trau.ini` is first seen after the upgrade stamp — a fresh
  clone, say — re-tiers to the new defaults instead of being migrated. Dating the
  upgrade is what keeps a deliberate provider default out of phase routing, and a
  file that post-dates it is indistinguishable from one written on purpose.
- A user who deliberately wanted "everything on sonnet" now has nine keys where
  they had one. The migration is what makes that visible rather than surprising;
  deleting them returns each phase to its default.

[COD-1333]: https://linear.app/codesomelabs/issue/COD-1333/replace-per-phase-routing-inherit-with-fixed-opinionated-per-phase
