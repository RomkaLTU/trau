# ADR 0041 — TEST_EFFORT defaults to low: happy-path tests unless a repo opts up

- **Status:** Accepted
- **Date:** 2026-08-07
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1568

## Context

`TEST_EFFORT` tells the build, repair and bugfix agents how much test-writing
effort to spend. It has four levels — `off`, `low`, `medium`, `high` — and shipped
defaulting to `high`, the inject-nothing level: the prompt fragment renders empty
and the agent tests as it sees fit.

Two problems with that as a default, both confirmed with the maintainer:

- **Cost and speed.** "As it sees fit" in practice means edge cases, error paths and
  permutations on top of the behavior the slice actually added. Those tests cost
  tokens to write and wall-clock to run, on every slice, in every repo that never
  touched the key.
- **Test bloat.** Agent-written suites at `high` accumulate noisy, low-value tests
  in the target repo. They are cheap to add and expensive to live with: every future
  slice reads and re-runs them.

The regression story does not depend on the level. Verify still runs the tests
relevant to the slice, so a break in what the slice touched still fails the verdict
regardless of how many tests the build agent wrote.

## Decision

**The built-in default of `TEST_EFFORT` changes from `high` to `low`.** Out of the
box, the build, repair and bugfix prompts carry the low fragment — cover the core
happy path of the changed behavior and nothing more, skipping edge-case, error-path
and permutation tests.

- **`high` restores the previous behavior exactly.** The inject-nothing convention
  is preserved: `testEffortNote("high")` still returns the empty string, so a repo
  that sets `TEST_EFFORT=high` gets prompts byte-identical to the old default. The
  opt-up path is the existing one — trau.ini, env, or the web Settings page.
- **No level semantics or fragment wording changed.** `off`, `low` and `medium` say
  exactly what they said before, including `off`'s verify-side sentence swap, which
  still fires only for `off`. Only which level a repo lands on when it says nothing
  is different.
- **This is a behavior change for every repo that never set the key**, applied
  without a migration or one-time notice. The release changelog carries the note:
  repos without `TEST_EFFORT` now get happy-path-only tests; set `TEST_EFFORT=high`
  to restore the old behavior.

## Consequences

- Cheaper, faster build/repair/bugfix passes by default, and target repos stop
  accruing agent-written edge-case suites nobody asked for.
- A repo that genuinely wants broad agent-written coverage must now say so. That is
  the intended trade: the expensive posture becomes opt-in rather than the silent
  default.
- Thinner agent-written suites mean marginally less safety net for future slices in
  repos that never opt up. Verify's relevant-test run and the rubric remain the bar
  that actually gates a slice, so the exposure is to regressions nobody would have
  written a test for anyway.
- This mirrors ADR 0040, which deliberately softened `VERIFY_EFFORT`'s default to
  `medium` for every repo on the same reasoning: the unbounded posture is worth
  having available, but it is the wrong thing to charge every repo for by default.
  `VERIFY_EFFORT` itself is untouched here.
