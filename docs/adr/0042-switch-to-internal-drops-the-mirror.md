# ADR 0042 — Switching a repo to the internal tracker drops the mirror, without tombstones

- **Status:** Accepted
- **Date:** 2026-08-07
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-1559

## Context

`TRACKER_PROVIDER=internal` means the hub itself holds the issues: nothing is
pulled from an external tracker and nothing is written back to one. A repo that
has been mirroring Linear, Jira, Azure DevOps or GitHub already carries that
tracker's tickets in the `issues` table with `source <> 'internal'` (ADR 0007).

Written as a bare key edit, the switch leaves that mirror in place. The result is
a repo that reads as internal but whose backlog is mostly rows no sync will ever
refresh again: they cannot be edited (the internal issue editor only touches
`source = internal` rows), they cannot be reconciled away (no reader resolves), and
they cannot be re-pulled. Worse, the identifier namespace is shared: the internal
sequence mints `PREFIX-N`, and a mirrored ticket may already hold `PREFIX-42` — so
once the mirror is invisible or gone, the next internal issue can be minted onto an
id that run history, checkpoints and queue records already point at.

Two hub write paths set the key for a repo: the per-repo settings write
(`PUT /api/v1/repos/{repo}/config`) and the project-wide tracker write
(`PUT /api/v1/projects/{project}/tracker`), which the onboarding wizard re-run also
lands on.

## Decision

**Both hub write paths run a guarded migration when the write sets
`TRACKER_PROVIDER=internal` and at least one affected repo still holds rows with
`source <> 'internal'`.** In order:

1. **Busy guard first, for the whole write.** If any affected member repo has a
   queue entry in a pending, paused or running state whose source is not
   `internal`, the entire write is refused with `409` and a structured body naming
   the blocking ids. Nothing is written and nothing is dropped — a project never
   ends up half-switched. This mirrors the `IssueBusyError` guard `DetachToInternal`
   already applies: live work is tracked under an identifier the migration would
   retire.
2. **The identifier sequence is lifted clear before the drop.** The floor is the
   highest number already spoken for under the repo's `InternalPrefix`, taken
   across *both* every `issues.identifier` row with that prefix (whatever its
   source) and every `queue_items.id` the root has ever carried. `issue_seq`
   advances to floor + 1 when that is higher; it never moves backwards. Computing
   it before the drop is the point — afterwards the mirrored ids are gone and the
   floor is unrecoverable.
3. **The mirror is dropped** per member repo via `Issues.DropSynced`: external rows
   deleted, comments cascaded, the dropped rows' blocked-by edges cleaned, the sync
   cursor emptied. Internal issues survive untouched.
4. **The key is then written.** Credentials and every other tracker key are left
   exactly as they were, as is the cached team/project binding.
5. **Nothing is written to the external tracker.** No labels, no comments, no
   transitions. The tickets stay as they are.

**No tombstones.** `DetachToInternal` tombstones each retired identifier so a later
sync cannot re-import a ticket beside the internal issue it became. A provider
switch is the opposite case: the tickets were not converted into anything, they
were simply un-mirrored, and switching back has to be able to re-import all of
them. Tombstoning would make the switch one-way.

**Switching back is a plain provider edit followed by a sync.** The cursor is
empty and the binding is cached, so the next sync is a full re-pull of the whole
project from scratch — exactly the force-resync path (ADR 0007), reached without a
force-resync.

## Consequences

- The switch is reversible by construction: drop without tombstones, cursor reset,
  binding and credentials kept. Nothing but the mirror itself is discarded, and the
  mirror is derived state.
- **The shared id namespace is the one caveat on switching back.** The seq advance
  protects against ids the repo has *already seen*. It cannot protect against ids
  the external tracker mints *after* the switch: work on the tracker continues, and
  a ticket created there later may land on a number the internal sequence has since
  handed out. The re-import then finds the identifier held by an internal row, and
  `Upsert` deliberately skips it rather than overwriting (ADR 0007) — so the ticket
  is silently absent from the re-pulled board. A repo that expects to switch back
  should set `ISSUE_PREFIX` to something the tracker never mints, which removes the
  collision entirely.
- **Only the hub write paths are hooked.** A provider edit made by hand in
  `~/.trau.ini` or a repo's `.trau.ini`, or by any path other than these two
  endpoints, still lands as a bare key write and leaves the mirror behind. The
  recovery is the existing one — force-resync after switching back, or a manual
  drop — and closing that gap at the config layer is tracked separately.
- The busy refusal is a real refusal, not a warning: a repo with a queued tracker
  ticket cannot switch until that entry is settled or removed. That is the
  conservative direction — the alternative is retiring the identifier a running
  child is mid-way through.
