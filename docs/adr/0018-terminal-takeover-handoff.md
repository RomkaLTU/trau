# ADR 0018 — Terminal takeover handoff

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** Romas (sole maintainer)

## Context

Every agent phase runs in a fresh, isolated claude session whose id is minted
by the loop child, passed as `--session-id`, used once after the call for token
recovery, and discarded. When a run goes sideways mid-phase, the operator's
only options are to let the loop retry or to quarantine the ticket — there is
no way to step into the agent's actual session, with its full context, and
steer it by hand. Claude Code can resume a session by id, so the missing piece
is durability: the id must outlive the child process that minted it, and the
handoff must not let a human terminal and the loop fight over the same working
tree.

## Decision

The per-phase claude session id becomes part of the ticket's checkpoint: at
every claude phase start, before the phase's terminal session spawns, the loop
writes `SESSION` (the uuid) and `SESSION_PHASE` (the phase label, e.g. `build`,
`repair2`) through the existing checkpoint store, so the fields reach the hub
with the same guarantees as every phase transition and survive a SIGTERM at any
point mid-phase. Phases routed to codex/kimi leave the fields untouched — the
checkpoint always names the most recent claude session.

On top of that handle, terminal takeover obeys four invariants:

1. **The takeover terminal never opens while a loop owns the repo working
   tree.** Stop-and-wait precedes launch: the run is stopped and its instance
   observed gone before the interactive session starts. Two writers on one
   checkout is the failure mode everything else exists to prevent.
2. **The takeover lock is a presence-registry instance** — a new session state
   `takeover`, PID-alive like every instance (ADR 0005). Never a lock file: a
   crashed takeover terminal must release the repo by dying, not by leaking a
   stale file that needs manual cleanup.
3. **Hand-back is manual.** Closing the terminal resumes nothing; the ticket
   stays parked and re-enters the loop only through Run next (ADR 0015). An
   automatic resume would race whatever state the human left half-finished.
4. **v1 resumes claude sessions only**, always the most recent claude phase's
   session. Codex/kimi expose no equivalent resume handle; a run whose current
   phase ran elsewhere takes over the last claude session instead.

## Consequences

- A stopped run is inspectable from the inside: `--resume <SESSION>` drops the
  operator into the interrupted phase's context instead of a cold checkout.
- The checkpoint gains two keys, registered beside the rest; they ride the
  existing atomic write, flow to the hub automatically, are readable via
  `GET /api/v1/repos/{repo}/runs/{ticket}/checkpoint`, and are cleared with the
  rest on Reset/Clear.
- Tracker calls (pick, status, …) share the claude backends but never touch the
  fields — only phase sessions are worth resuming.
- The presence registry gains a `takeover` state in a follow-up slice; nothing
  in this ADR ships a lock file or an auto-resume path.
