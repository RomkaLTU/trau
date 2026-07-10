# ADR 0005 — Live activity is reported by the instance, never derived from run artifacts

- **Status:** Accepted
- **Date:** 2026-07-10
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-764

## Context

The Hub's live surfaces (Overview, Watch) answered "what is this loop doing?"
by derivation: `activeRun` picked the newest non-terminal checkpoint under the
instance's runs dir and read its `PHASE`, with "in phase since" taken from the
state file's mtime. That broke visibly on 2026-07-10 (trucknet TMS-1139): a
build faulted, the checkpoint correctly held `PHASE=building` **and**
`FAILURE_CLASS=faulted` — phase records how far a run got, failure class why it
stopped — but the derivation read only `PHASE`, so the Overview showed a
17-minutes-and-counting "building" card, an "active loops: 1" tile, and a
"TMS-1139 faulted, needs attention" row about the same ticket at the same time.
The same shape of lie existed twice more: any interactive TUI (even idling on
the main menu) registered as an "active loop" decorated with whatever stale
in-flight checkpoint had the newest mtime, and the Watch view let bare
instance-liveness trump the fault variant.

The deeper problem is that run artifacts cannot answer the question. A
checkpoint is the durable state of a *ticket*; what a live *process* is doing
(executing a phase, grazing for the next ticket, parked on the fault recap,
idling at the menu) is a fact only that process knows, and no artifact
inspection can distinguish those cases.

## Decision

The instance reports its own session state through the registry entry it
already heartbeats: `idle | grazing | working(ticket, phase) | parked(ticket) |
stopping`. The Hub echoes what is reported and **never derives** live activity
from run artifacts; `activeRun` and the mtime-based `phase_since` are deleted.

Boundaries that keep the two truths from ever disagreeing again:

- **The entry says only *that* a session parked, never why.** Failure class and
  reason stay owned by the ticket's checkpoint; surfaces join the two. The same
  fact never lives in two places.
- **No derivation fallback.** An entry without a session state (older binary)
  renders as `unknown` — honest, and hub + TUI ship as one binary via autostart,
  so skew is transient.
- **Liveness stays pid-only** (`signal 0`); the heartbeat stays write-only. A
  stale-heartbeat policy was considered and rejected: reaping a suspended
  process would drop the repo-is-live guard that keeps web resumes safe.
- **Runs stay file-first.** `collectRuns` and the board still read checkpoints
  and work identically whether the loop is live or dead (ADR 0003's
  repos-outlive-loops posture is untouched). This ADR flips only the *live*
  side.

## Consequences

- The registry entry format gains `session_state`, `ticket`, `phase`,
  `state_since`; `Handle` gains `SetState`, and the TUI/loop must report
  transitions (register → idle, pick → grazing, phase change → working, stop
  short → parked, graceful stop → stopping).
- "Active loops" means `grazing | working | stopping`; parked and idle
  instances are visible but never counted as active.
- A future reader wondering why the instances API doesn't just read the
  perfectly good checkpoint files: that was the original design, and it is the
  thing this ADR removes.
