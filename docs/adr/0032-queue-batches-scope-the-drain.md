# ADR 0032 — A Batch is a scope on the armed queue, not a second drain

- **Status:** Accepted
- **Date:** 2026-08-02
- **Deciders:** Romas (sole maintainer)

## Context

A Repo's Queue is all-or-nothing to run: Start arms the drain and it works down
every runnable row until the queue is dry. An operator who wants only part of the
queue run today has two bad options — remove the rest and re-queue it afterwards,
losing each row's provider override and queue stamp, or watch the loop and Stop it
at the right moment, which nobody can do reliably for a run measured in hours.

The obvious shape — give a batch its own queue, its own drain flag and its own
loop — buys a second state machine that has to answer every question the queue
already answers: what happens when a member pauses, when a release gate holds the
repo, when the hub restarts mid-run, when a duplicate epic covers a member. Two
machines that must agree on all of that will eventually disagree, and the drain is
the component where disagreement means an orphaned child or a deadlocked queue.

## Decision

**A Batch is a subset label on queue items plus a scope field on the armed queue.
The drain stays one state machine.**

`queue_items.batch` records membership ('' = none, one batch per item);
`queue_repos.batch` records the scope the current drain was armed with ('' = the
whole queue), stored beside `draining` so a hub restart resumes a scoped run the
same way it resumes an unscoped one. `queue_batches` holds only the grouping's
identity — a per-root slug and an optional display name.

The scope changes exactly one decision in the drain: which rows `firstUnblocked`
may pick. Settle and pause classification, the duplicate-epic dedup (still judged
against the whole queue — a member an epic elsewhere already shipped is still a
duplicate), the child spec, the release gate and the self-reload wait are all
untouched, so a batched run's child is indistinguishable from any other queued
run's.

The scope's lifecycle follows the gesture that set it:

- whole-queue `Arm` **clears** it — Start is batch-blind and drains everything;
- `ArmBatch` sets it, and a no-resume start restarts the members alone;
- `Pause` and `Rearm` **preserve** it, so a parked member — and the auto-resume
  re-attempt of a blameless one — continues the batch rather than widening the run
  to the queue;
- `FinishDraining` judges the scope alone and **clears** it with the flag, so a
  batch that ran dry disarms even while non-members are runnable, and the next
  start is a fresh choice of scope.

That last rule is the one inversion: `FinishDraining` used to refuse to disarm
while anything queue-wide was runnable. It now applies that refusal to the armed
scope, which is the same rule for an unscoped drain.

## Consequences

- ADR 0029's start/stop/remove-only web loop control grows one verb, **Start
  batch**. It is a scoped Start, not a third halt: Stop still ends a batched run
  and per-item Remove still empties a queue.
- A batch-scoped drain that reaches its boundary stops with items still queued,
  which reads exactly like a Stop nobody made. The hub appends a
  `queue_batch_finished` event carrying the batch, its name and how its members
  settled, so the feed explains it.
- Starting a batch refuses when a runnable member is blocked by an unshipped
  ticket the batch does not carry: the drain would wait on a blocker its scope
  will never run. A blocker inside the batch is fine — draining it in queue order
  is what resolves it.
- Dismissing a batch touches no status and no running child, so the grouping is
  safe to undo mid-run. A batch whose members all left the queue stays listed
  until dismissed.
- `/queue/batches/{bid}` and the literal `/queue/{id}/move` and `/queue/{id}/run`
  patterns conflict in `net/http`'s mux — both match `/queue/batches/move` and
  neither is more specific — so the per-item actions moved behind one
  `/queue/{id}/{action}` dispatcher, the same shape `issues/{id}/{action}` already
  uses for the same reason. An action neither names now answers 404 in JSON rather
  than the mux's plain text.
- The queue view gains `batches`, a per-item `batch`, and `draining_batch`, so a
  client can label a scoped run. No UI consumes them yet.
