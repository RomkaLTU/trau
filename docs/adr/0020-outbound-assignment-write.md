# ADR 0020 — Assignment is the one outbound write on an explicit gesture

- **Status:** Accepted
- **Date:** 2026-07-22
- **Deciders:** Romas (sole maintainer)
- **Amends:** [ADR 0014](0014-assignee-reflection-and-credential-derived-identity.md)

## Context

ADR 0014 made assignee a reflected value: it arrives with the issue on sync,
it never goes back. That held while the board only *displayed* people. It
stops holding the moment an operator wants to hand a ticket to a teammate
from the trau board — the alternative is a context switch into Linear or Jira
to change one field, and back.

The read-only rule also left a second gap. The assignee facet is a DISTINCT
over the repo's stored issues, so it can only ever name people who already
hold work here. A picker built on it cannot offer the teammate you are trying
to assign the ticket *to* — precisely the case that matters.

## Decision

**Inbound sync stays authoritative; the hub may write assignment outbound on
an explicit user gesture.** Nothing else about assignee changes direction:
sync still overwrites the stored value each cycle, including back to NULL,
and a hub write that the tracker later contradicts loses. The write is never
inferred, batched, or performed by a phase — only a person clicking assign
produces one.

**The tracker is written first, the store second.** `PUT
/repos/{repo}/issues/{id}/assignee` calls the tracker's assignment API and
only mirrors into the hub's issue store on success; a refused write leaves
the row untouched and answers an error. The board therefore never shows an
assignment that does not exist upstream. The mirror trusts the client-sent
display name until the next sync reconciles the canonical one, because the
alternative — a second round-trip to read the name back — buys nothing the
sync cycle does not already fix.

**Assignable users are looked up live and never persisted.** `GET
/repos/{repo}/assignable-users` asks the tracker (Linear workspace users,
Jira `/user/assignable/search` scoped to the repo's project) on each request.
ADR 0014's ruling stands unchanged: no users table, no member-list sync, no
tombstones or permissions to keep straight. A page of results behind a name
query is all a picker needs.

**Identity still never leaves the hub.** Assignable-user rows carry the same
`me` boolean the facet and the issue rows carry, computed server-side against
the repo binding's resolved identity.

**Providers without an assignment API degrade, they do not fail.** Internal
and GitHub repos answer both endpoints with a typed capability error mapped
to a 409, so a surface hides the affordance rather than showing an error.
Internal issues stay Unassigned — they have no people behind them.

**Assignment remains behaviorally inert.** It does not influence the picker,
the queue, or loop eligibility, exactly as ADR 0014 decided. This ADR moves
one direction of data, not the execution model.

## Considered options

- **Keep reflection strictly one-way** — the honest reading of ADR 0014, and
  the reason this is an amendment rather than a quiet feature. Rejected: the
  rule was written to avoid modeling people, not to forbid a field write, and
  paying a context switch to set one field is a bad trade.
- **Mirror first, write to the tracker in the background** — a snappier
  board, at the cost of an assignment that can silently never land. The
  tracker is the system of record for who owns work; a write that fails must
  be visible immediately.
- **Sync a member list to back the picker** — the users table ADR 0014 already
  rejected, revived to save a request per picker open. Still not worth a new
  sync surface with its own pagination, permissions and staleness.

## Consequences

- The tracker credentials now need write access to the assignee field. A
  read-scoped token surfaces the failure at assign time, not at sync time.
- Assigning to someone the tracker will not accept (no project permission)
  fails at the tracker, which is the correct place for it to fail.
- The picker costs one live tracker request per open. Acceptable: it is a
  deliberate gesture, not a page render.
- ADR 0014's glossary entries are unchanged except that **Assignee** is no
  longer "never written back" — it is written back only on an explicit
  gesture.
