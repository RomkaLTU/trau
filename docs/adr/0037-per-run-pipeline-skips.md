# ADR 0037 — Per-run pipeline skips, with build/commit/PR locked

- **Status:** Accepted
- **Date:** 2026-08-06
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1519

## Context

`docs/verify-checks.md` opens with "Trau's pipeline is fixed", and until now that
was literally true: every run walked build → handoff → verify → commit → PR → CI →
merge, and the only way to drop a step was a config key that changed it for *every*
later run in the repo — `LINT_FIX=0`, `CLEANUP=0`, `REQUIRE_CI=0`, `AUTO_MERGE=0`.
Verify had no key at all.

That is the wrong granularity for the cases operators actually hit. A trivial
one-line slice does not earn a cold verifier turn. A repo whose CI is down for the
afternoon still wants its work delivered. A change an operator intends to review by
hand wants the PR opened and left alone. Reaching for the config key to get any of
those makes a permanent decision to answer a per-run question, and whoever forgets
to put it back silently lowers the bar for every run after.

The obvious alternative — let the operator name the work to skip on the command
line — has two hazards. It can be forgotten across a resume, so a run that skipped
verify on Monday re-enters verify on Tuesday and grades a diff nobody wrote a brief
for. And it can be laundered: work delivered with no verifier behind it that reads,
in the PR and on the ticket, exactly like work a verifier passed.

## Decision

**A run may bypass named Activities. The Step that produces the deliverable may
not, and every bypass is on the record.**

- **The skip set is per-run, never configuration.** `--skip <keys>` is an internal
  flag on the loop child (the hub spawn path passes it; it is in no user
  documentation). It becomes `config.Options.Skips`, then `pipeline.Pipeline.Skips`
  — a run option handed in at the composition root. It is deliberately *not* a
  `config.Config` field: nothing in the ini precedence chain may set it, so no repo
  can quietly acquire a permanently lowered bar.
- **Five canonical keys, validated at startup.** `lintfix`, `cleanup`, `verify`,
  `ci`, `merge`. Each names exactly one Activity, or — for `verify` — one whole
  Step. An unknown key fails the run before it starts, naming the valid set, so a
  typo can never read as "skip nothing" and let work through the bar the operator
  thought they had lowered.
- **Build, commit and PR carry no key.** They are what makes a run a run: a run
  that writes no code, records no commit, or opens no pull request has produced
  nothing for the skip to be a trade-off against. Locking them keeps the skip set a
  statement about *review depth*, which is a judgment call, rather than about
  whether the run happened at all, which is not.
- **The set is durable on the run's own checkpoint.** It is written to the `SKIPS`
  key at run start and read back when a resume carries no argv, so a resumed run
  honors the same skips and — the case that matters — never re-enters a Verify Step
  the first attempt already declined. The effective set is resolved per unit of
  work — each ticket, and the epic release the same `Pipeline` performs after them —
  so one ticket's stored skips never leak onto the next thing the loop does.
- **Skipping verify advances the checkpoint to `verified`.** The handoff brief, the
  mechanical test gate, the verifier, the repair and bugfix turns, and the
  browser-verify gate are all bypassed together — a brief exists only for a cold
  verifier to grade against, so half a Verify Step is worse than none. No rubric,
  verdict, lessons or proofs are produced, and every downstream consumer already
  tolerates their absence.
- **A skipped verify never looks like a passed one.** The PR body's Testing section
  and the ticket's QA note both state that verification was skipped by the operator
  and that the slice needs manual QA, in place of the verify facts they normally
  carry. A `run_skips` event records the set for whichever ticket it applied to, and
  the run read side (`hubstore.CheckpointRow` → `RunView` → `RunDetail`) exposes it
  so the board can mark the run later.
- **`ci` and `merge` reuse the paths that already exist.** Skipping `ci` makes the
  merge gate behave exactly as `REQUIRE_CI=0`; skipping `merge` takes exactly the
  `AUTO_MERGE=0` path, so the run ends awaiting-merge with the PR open. That holds
  at every merge gate trau has — single repo, each child of a Folder repo, the epic
  PR, the epic's local squash and a stacked epic's whole stack — since a merge trau
  performs anywhere is the one thing the key exists to prevent. Neither invents a
  second way to do something the pipeline already does one way.

## Consequences

- The pipeline is no longer fixed, and `docs/verify-checks.md` now says so.
- A slice delivered under `--skip verify` reaches `merged` with no verdict on its
  checkpoint. Anything that reads a run's verdict must keep treating absence as
  "none recorded" rather than as a failure — which is what it did before, since a
  faulted run has no verdict either.
- The error taxonomy is untouched: a skipped Activity is not an outcome, so
  `classifyPhaseErr` never sees one.
- The skip set is honored on resume even by an operator who did not ask for it on
  that invocation. That is the point — a skip is a property of the work, not of the
  invocation — but it means clearing a skip needs the checkpoint cleared
  (`--clear`), not merely a re-run without the flag.
