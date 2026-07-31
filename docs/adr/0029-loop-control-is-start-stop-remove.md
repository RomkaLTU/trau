# ADR 0029 — Loop control is Start, Stop and per-item Remove

- **Status:** Accepted
- **Date:** 2026-07-31
- **Deciders:** Romas (sole maintainer)

## Context

The Loop page carried two loop-level halts. **Stop** disarmed the drain and
ended the running child, parking the item at its checkpoint with every row left
queued. **Shut down** did that *and* dropped the running and paused items'
checkpoints and emptied the queue — a second verb whose only real difference was
destruction the user could not undo or partially apply.

They were hard to tell apart at the moment of use. Both sat side by side under
the same card, both read as "make it stop", and only the confirm copy
distinguished "resumable from here" from "the checkpoints are gone". The
teardown also carried machinery nothing else needed: an in-flight `shuttingDown`
flag on the wire, a race window watching for a child the drainer spawned as the
disarm landed, and a bulk checkpoint drop that had to sequence behind a confirmed
process death before it was safe to run.

ADR 0015 had already collapsed web launching onto one gesture, and LOOP-76 had
just removed the Reset / Clear / Reconcile checkpoint controls for the same
reason: a maintenance verb the UI offers is a verb the UI must keep correct in
every state.

## Decision

**Loop control is Start, Stop, and per-item Remove. There is no separate
shutdown.**

Stop is the single loop-level halt: it disarms the drain and ends the in-flight
child through the existing escalation, the item parks at its last checkpoint,
and every other row stays queued. Start picks the queue back up from there. The
button reads `Stop` — the same verb the overview and run view already surface
for a live instance.

Emptying a queue is Stop followed by per-item Remove, which already stops a
running row's child before dropping it and already spells out that the ticket
survives.

`POST /queue/shutdown` and the MCP `shutdown_queue` tool are deleted along with
`shutdown.go` — `beginShutdown`, `teardownQueue`, `stopRaceSpawned` and the bulk
checkpoint drop — and `shutting_down` leaves the queue wire type. `stopKillGrace`
survives as the grace a Stop and a running-item Remove share.

## Consequences

- `/queue/{id}` covers the path the deleted route left behind, so the item route
  answers 404 for an id it holds no row for rather than a 405 that advertises
  DELETE on a path that is gone.
- No web or MCP surface drops a checkpoint in bulk. A checkpoint goes when its
  ticket's queue row is removed by hand or when a run settles — never as a side
  effect of halting the loop.
- Clearing a long queue is now several gestures instead of one. Accepted: the
  one-gesture version was the only way to lose paused work by accident.
- `pause_queue` is unaffected. It is the drain-only halt an external agent
  reaches for — it lets the current item finish rather than ending it — and has
  no UI caller by design.
- The MCP `dequeue` refusal for a running item now points at `stop_instance`,
  the tool that actually ends the child, rather than at a tool that no longer
  exists.
- `CONTEXT.md`'s **Stop** entry ("ending a live run from the TUI") is now the
  web verb too; the glossary distinction that mattered — Stop ends a live run,
  Quit exits the app — is unchanged.
