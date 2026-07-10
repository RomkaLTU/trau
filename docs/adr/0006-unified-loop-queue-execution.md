# ADR 0006 — The Loop launches a single ordered queue of tickets and epics

- **Status:** Accepted
- **Date:** 2026-07-10
- **Deciders:** Romas (sole maintainer)

## Context

The Loop screen used to offer a two-way scope switch: run a repo's *ready
queue* (graze every ready-for-agent ticket) or drive *one epic*. A separate
Queue screen — added for the web hub — let an operator register a mix of tickets
and epics from the Backlog board and have the hub drain them one run at a time.
Two screens, two mental models, and the Backlog board straddling both as the
only place to enqueue.

The redesign collapses this to one model: the operator builds **one ordered
queue** on the Loop card by adding tracker ids (ticket or epic), reorders them,
and starts it. The order shown is the execution order. Epics expand into their
remaining sub-issues at run time. This is what an operator actually wants —
"do these, in this order" — without choosing a scope first.

The execution semantics the redesign asks for (a global dedup set, a resumable
position, on-fault halt-or-skip) read like they need a new single-process queue
runner in the binary. They do not.

## Decision

**The web hub's existing per-repo drainer is the queue executor.** It already
runs the persisted `queue.json` strictly top-to-bottom, one child per item
(ticket → `--parent <id> --once`, epic → `--parent <id>`), reconciles each
child's outcome through the failure taxonomy, and re-reads the file every tick
so a serve restart resumes in place. The full spec is reached by extending it,
not by adding a parallel execution mode — which keeps `queue.json`
**single-writer** (the hub) and leaves `runLoop`'s per-run scoping untouched.

The gaps the redesign named map to localised additions:

- **Reorder.** `queue.Store.Move(id, dir)` swaps a pending item one slot; it
  refuses to move — or jump over — the running item, so a reorder never
  disturbs the run in flight.
- **Kind auto-detect.** Enqueue accepts a bare id: the hub validates it against
  the repo's tracker (best-effort, refusing a cross-project ticket), then lists
  its children — any child makes it an epic carrying them, none a ticket. The
  Loop card adds an id without knowing what it is.
- **Global dedup.** A standalone ticket an *earlier* queued epic already covers
  is marked `skipped` (first occurrence wins), never run twice. Epics dedup
  their shared leaves naturally through tracker state as they run, so only a
  standalone ticket can be a queue-level duplicate.
- **On-fault policy.** Default `halt` parks the faulted item and stops the drain
  for a human (unchanged). `skip` settles it failed and drains on. A provider
  *pause* always parks regardless — it is blameless, not a fault to skip.
- **Skip-resume.** Starting with `no_resume` calls `Store.Restart` — every
  non-running item back to pending, the executed counter zeroed — and passes
  `--no-resume` to children so stale per-ticket checkpoints are ignored too.

**A child reports its own outcome.** A headless queue-member child writes a
small `DrainReport{class, reason}` (a hidden `--drain-report <path>` the drain
passes; one path per repo, safe because the drain is strictly sequential). The
drain reads it to settle the item. The report is authoritative for a **pause or
fault** — this is what lets an epic be parked when its fault lives on a
*sub-issue's* checkpoint, which the epic's own checkpoint never shows — while a
give-up and a clean finish fall through to the checkpoint-derived outcome, which
already reads those correctly.

## Consequences

- `queue.json` gains run-level fields (`no_resume`, `on_fault`) and a `skipped`
  item status. The file stays the hub's to write; children touch only the
  per-repo drain-report and their own per-ticket checkpoints.
- The per-ticket `state` format and its phase ranking are **untouched** (the
  stability invariant holds). The cross-item cursor is not a new state file: it
  is derived from item status — the first pending-or-paused item is the cursor,
  and a re-attempt resumes its own per-ticket checkpoint.
- The Loop card owns build + start + monitor; the standalone Queue and Backlog
  screens are retired (issue creation moves under Author, discovery moves to the
  card's eligible picker). Reversible — they are a route and a nav entry.
- A future reader wondering why the queue is not a first-class `trau --queue`
  mode: it was considered and rejected. A single-process runner would be truer
  to the pseudocode but would refactor `runLoop`'s scoping and make `queue.json`
  a two-writer file across processes, for semantics the sequential drainer plus
  a per-child report already deliver.
