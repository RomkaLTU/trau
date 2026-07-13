# ADR 0009 — Present-tense Activity is its own signal; Checkpoint phases stay past-tense

- **Status:** Accepted
- **Date:** 2026-07-13
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-837

## Context

The web Loop screen answers "what is this run doing?" from the ticket's
Checkpoint `PHASE` — a value written *after* work completes (`building → built
→ handed_off → verified → pr_open → merged`, ranks in `internal/state`). The
stepper renders the current rank as the active step, so after build it runs
one step behind reality: while the checkpoint says `handed_off` the run is
inside the verify → repair → bugfix loop, yet the UI shows "handoff" active;
while it says `pr_open` the run is waiting on CI, yet the UI shows "pr".
COD-837's field observations — "90% of the time build", "handoff looks like
verify", "PR and merge barely visible" — are all this one mechanism.

The mistake is semantic, not mechanical. A Checkpoint records how far a
*ticket* durably got, for resume — it is necessarily past-tense, and its
ranking is a declared stability invariant (AGENTS.md). What a live run is
doing *now* is a different fact with a different owner and lifetime. The TUI
already holds the present-tense fact (its stepper is driven by `PhaseStart`
callbacks) but that signal never leaves the process; meanwhile the presence
heartbeat (ADR 0005, mechanism per ADR 0008 §7) reports the checkpoint value
as the "phase" of a Working session — a past-tense answer to a present-tense
question.

Three phase vocabularies coexist: nine routable agent-call keys
(`internal/agent/router.go`), six-plus checkpoint ranks (`internal/state`),
and two different display step lists (TUI seven, web five).

## Decision

Split the two facts and name them; regroup both displays around where
wall-clock actually goes.

- **Checkpoint phase** stays exactly as is — past-tense, durable,
  invariant-protected. Nothing about resume changes.
- **Activity** is the new present-tense fact: the pipeline work a Working
  session is executing right now, one of `build, lintfix, cleanup, handoff,
  verify, repair, bugfix, commit, pr, ci-wait, merge`, plus a free `detail`
  (the raw call label, e.g. `repair2`). It exists only while the session is
  Working; Grazing/Idle/Parked carry none. It is reported by the instance in
  its presence heartbeat (extending ADR 0005's working state), echoed
  verbatim by the hub, and additionally emitted as an `activity_change` event
  through hubclient (ADR 0008 single-writer path) so per-activity wall-clock —
  including non-agent time like CI wait, invisible to `agent_call` durations —
  derives from timestamp deltas. Durations are never stored; they are always
  derived. A single writer in the pipeline sets the Activity; during the
  concurrent build tail (lintfix → cleanup ∥ handoff brief) last-started wins,
  which cannot flap the display because the whole group belongs to one Step.
- **Step** is a display grouping of Activities, the same three everywhere
  (web and TUI): **Build** = build + lintfix + cleanup + handoff, **Verify** =
  verify + repair + bugfix, **Ship** = commit + pr + ci-wait + merge. The
  boundary follows the concurrency structure: since COD-796 the handoff brief
  runs concurrently with the cleanup chain, and concurrent work cannot
  straddle sequential Steps, so the brief sits in Build. The Activity→Step map
  lives at the display edge (one Go const for the TUI, one TS const for the
  web) and never crosses the protocol — regrouping steps later is a display
  change, not a protocol change. The Activity set is not the router key set:
  `ci-wait` and `merge` have no agent call, and routing is untouched.

Compatibility: a heartbeat without an Activity (older binary) renders from
the checkpoint through the *corrected* interpretation — `handed_off` ⇒
verifying, `verified`/`pr_open` ⇒ shipping — strictly more honest than the
display it replaces. This stays inside ADR 0005's no-derivation boundary: it
interprets the run's own reported checkpoint; it does not inspect artifacts
to invent a session state.

## Consequences

- The heartbeat and `GET /instances` gain `activity` + `detail`; the events
  store gains `activity_change`.
- Web and TUI steppers both collapse to Build/Verify/Ship with a live
  sub-activity label ("Verify · repair 2"); the TUI's seven-step stepper and
  the web's five-step `PHASE_SEQUENCE` go away, retiring the third and fourth
  phase vocabularies. Repair loops and CI wait become visible for the first
  time.
- Run detail's per-phase cost table gains a derived Duration column grouped
  by Step (read-time derivation from `activity_change` deltas).
- Slices: COD-860 (signal core, blocker), COD-861 (web stepper), COD-862
  (TUI stepper), COD-863 (durations).
- A future reader wondering why the live stepper doesn't just read the
  perfectly good checkpoint: it did, and the off-by-one lie it produced is
  the thing this ADR removes.
