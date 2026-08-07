# ADR 0040 — VERIFY_EFFORT: verification strictness is a repo-level dial, default medium

- **Status:** Accepted
- **Date:** 2026-08-07
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1557

## Context

The verify prompt has always asked for one thing: cold, adversarial QA. Grade the
rubric, run the slice's tests, and hunt for anything that does not work. That
open-ended hunt is the moat — it is what catches the defects a rubric author never
thought to name — but it is also unbounded, and an unbounded bar interacts badly
with an automated repair loop.

COD-1544 is the worked example. The feature itself passed every time: verify #1 and
verify #3 each confirmed all fifteen acceptance criteria held live. What failed the
verdicts was collateral the hunt turned up — a stale existing test, a leaked
skip-set write on a refused start, a clobbered per-run provider override, a
reordered queue, and finally a scope violation. Each repair fixed the last blocker
and surfaced the next, across four verify→repair cycles and $142.60.

`TEST_EFFORT` already lets a repo say how much test-writing effort the build,
repair and bugfix agents should spend. Verification had no equivalent: the bar was
whatever an adversarial agent chose to look at that run.

## Decision

**`VERIFY_EFFORT` is a repo-level config key with three levels — `low`, `medium`,
`high` — defaulting to `medium`. The level narrows both what the verifier
investigates and what may fail the verdict.**

- **The level governs depth, not just leniency.** A lower level does not merely
  forgive what the hunt finds; it tells the verifier not to run the hunt. That
  makes a cheaper level genuinely cheaper and faster, not a full-price
  investigation with a softer grader.
  - **low** — the rubric is the entire job. Grade every `acceptance_criteria`
    entry, run the `required_tests`, exercise the `ui_paths`, and fail only on a
    failed criterion, a failing required test, a `fail_condition` holding, or an
    implemented `non_goal`. No regression sweep, no edge-case exploration, no
    code-smell audit.
  - **medium** — the rubric contract plus the slice's own footprint: existing tests
    covering the changed files must still pass, and behavior the diff directly
    exercises must not break. No hunting beyond the diff.
  - **high** — today's behavior, unchanged. The level renders an empty fragment, so
    the verify prompt is byte-identical to a repo that never set the key — the same
    convention `TEST_EFFORT=high` follows.
- **The rubric contract blocks at every level.** The rubric is what the ticket
  author actually wrote down; nothing about spending less on verification makes it
  optional. Calibrated against COD-1544: retry3 (implemented a stated non-goal)
  blocks even at `low`; verify #1's broken existing test and retry2's clobbered
  override in changed code block at `medium` and `high`; retry1's `no_resume`
  interaction edge case is the class of deep hunt only `high` performs.
- **The default is `medium` — a deliberate softening for every repo**, not an
  opt-in. The COD-1544 pattern is not exotic; it is what the full hunt does to any
  slice large enough to touch adjacent code. Repos that want the old bar set
  `VERIFY_EFFORT=high` explicitly.
- **There is no `off`.** Skipping verification for a single run already exists as
  the per-run `verify` step skip (ADR 0037), which also makes the skip visible in
  the PR body and the ticket's QA note. A config value that silently disabled
  verification for every later run is exactly the permanent answer to a per-run
  question that ADR 0037 rejected.
- **Observations beyond the level's bar are recorded, not discarded.** Anything the
  verifier notices past its level goes into the verdict's existing `summary` prose
  as an explicitly non-blocking note. No verdict schema change and no new UI: notes
  surface in the attempt log operators already read.
- **Repo-level only, like `TEST_EFFORT`.** ini, env, and the web Settings page. The
  per-run surface stays skips-plus-provider (ADR 0037); a per-run effort picker can
  be a later decision.
- **The mechanical test gate is unaffected.** `testGate` only checks that the tests
  the rubric names exist and that tests the slice itself changed survive their
  `-race`/repeat runs. It is cheap, deterministic and rubric-derived, so it runs at
  every level.
- **Verify panel members inherit the level.** Members render through the same
  `verifyAttempt` path as the single verifier, so the fragment reaches each one with
  no separate plumbing.

The key is catalog-driven (ADR 0011): the enum validation on the web PUT, the
Settings-page select and the TUI picker all come from the catalog entry's
`Options`, with no bespoke UI code.

## Consequences

- Every repo's verify bar moves on upgrade unless it opts back into `high`. Slices
  that used to fail on collateral outside the rubric and the diff now pass with a
  note in the summary. That is the intended trade: fewer repair cycles bought with
  a narrower hunt.
- `VERIFY_EFFORT` sits alongside `CLAUDE_VERIFY_EFFORT` / `CODEX_VERIFY_EFFORT`,
  which set the *provider's reasoning effort* for the verify phase. The adjacency
  was accepted on purpose — the key is the exact mirror of `TEST_EFFORT`, and any
  disambiguating name would have broken that symmetry — so the catalog description
  and `trau.ini.example` both spell the difference out.
- The two verify prompt fragments compose rather than compete: `TEST_EFFORT=off`
  rewrites the which-tests-to-run sentence, `VERIFY_EFFORT` appends the strictness
  paragraph, and they occupy different template slots.
