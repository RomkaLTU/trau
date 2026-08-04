# ADR 0034 — Web requeue is a step on a fix-session receipt

- **Status:** Accepted
- **Date:** 2026-08-04
- **Deciders:** Romas (sole maintainer)

## Context

A fix-mode interview diagnoses a failed run and rewrites the ticket's guidance
for the next attempt. Applying that outcome leaves the ticket in exactly the
state the requeue exists for: the guidance is better, but the tracker still
carries the quarantine label, the attempt PR is still open, its branch is still
there, and the checkpoint still records the give-up. Until all four are undone
the picker skips the ticket, so the whole point of the session — try again with
better instructions — needed a trip to a terminal for `trau --requeue <ID>`.

Requeue is not a new capability. `(*Pipeline).Requeue` has always done the four
steps, and the Loop page's quarantine banner already drives it for the ticket
that halted the drain. What was missing was the offer at the moment the user has
just decided the ticket deserves another run.

The pull the other way is ADR 0029: loop control is Start, Stop and per-item
Remove, and every maintenance verb the UI offers is a verb the UI must keep
correct in every state. A general "requeue anything" surface — a button on every
failed run, a bulk action over the needs-you strip — would be exactly the kind
of always-available verb that ADR removed.

## Decision

**Web requeue exists only as an approval-gated step on an applied fix-session
receipt.** The button appears on the outcome review's applied card when the
session's mode is `fix`, whatever disposition it reached — a `no_change`
("transient failure, retry") is the case most worth requeuing. It appears
nowhere else: no per-run action on the board, no bulk gesture, no MCP tool.

`POST /api/v1/repos/{repo}/runs/{ticket}/requeue` spawns the hub's own binary
with `--requeue <ticket>` in the repo root and answers with the steps that
changed, so the receipt lists what it actually did. It refuses only on the
ticket itself — queued, or running here — rather than on the whole repo, so a
rewritten ticket can go back in front of a loop already draining other work; the
drainer picks it up naturally, the same way it would after a CLI requeue. A run
that is no longer failed is nothing to do rather than an error.

**`--force` never crosses to the web.** An attempt PR that already merged is
refused with the child's own actionable text, and the CLI stays the only way to
override it.

**ADR 0029's rule is unchanged.** The receipt links to the Loop page rather than
arming the drain: Start, Stop and per-item Remove remain the only loop control,
and requeue makes a ticket eligible without deciding when it runs.

## Consequences

- Requeue reaches the browser without becoming an ambient verb. The approval
  gate is the fix session itself: someone read the diagnosis and applied it.
- The Loop page's quarantine-banner requeue stays as it is, refusing on the
  whole repo. The two are deliberately different: that one is a halt to clear
  before the loop can move at all, this one is a decision about one ticket.
- Every step is idempotent, so a partial failure — a remote branch the forge
  refused to drop — surfaces the error and a retry click is safe.
- A ticket whose attempt merged has no path back through the web. Accepted: at
  that point the code shipped and there is nothing to retry.
