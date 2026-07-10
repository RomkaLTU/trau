# ADR 0007 — Hub SQLite database: authoritative for hub state, derived for run artifacts

- **Status:** Accepted
- **Date:** 2026-07-10
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-770 (epic; slices COD-771…COD-783)
- **Amends:** ADR 0003 (§3 "No SQLite")

## Context

ADR 0003 rejected SQLite for hub state and committed to the filesystem as the
source of truth, with an explicit escape hatch: if JSONL read performance ever
became a measured problem, any store introduced must be a rebuildable cache.

We measured (2026-07-10, live hub). The file-backed lists are not the problem:
every filesystem-served endpoint answers in ≤45 ms, and the entire structured
corpus across all repos is under a megabyte. The list that hurts is the
backlog — 2.5–6.6 s per fetch — because `TeamBacklog` cursor-walks the whole
tracker team through sequential API round-trips on every request and filters
to the Project only after download. That cost grows with total team issues and
no local storage engine touches it.

At the same time, the roadmap wants capabilities the filesystem cannot
reasonably serve: full-text search over tickets, filtering, periodic tracker
sync into a local store, paginated queries over run history — and, direction-
setting for the product, **internally-created issues**: trau growing its own
lightweight issue tracking so a repo can run the loop with no external tracker
configured, with Linear/Jira demoted from request-time dependency to sync
source. Two premises of ADR 0003 have also aged:

- The binary-weight objection assumed cgo-era SQLite. Pure-Go drivers
  (modernc.org/sqlite; ncruces/go-sqlite3) keep `CGO_ENABLED=0` and the ADR
  0002 release matrix intact at performance parity for our scale.
- Hub-owned state has multiplied (`repos.json`, `workspace.json`,
  `queue.json`, per-repo drain state), all hand-rolled read-modify-rewrite
  JSON with a package mutex.

There are zero external users, so storage moves need no compatibility shims.

## Decision

### 1. One global, hub-owned database

The hub owns `~/.trau/trau.db` (under `TRAU_HOME`), opened at serve startup:
WAL journal mode, busy timeout, foreign keys on. **Only the hub process opens
it.** Loops, `trau <ID>` runs, and the TUI never touch the database; they keep
appending run artifacts to files exactly as today. The hub is already a
machine singleton (ADR 0004), so this yields single-writer semantics without
any cross-process locking.

Driver: modernc.org/sqlite (pure Go, database/sql, FTS5 included).
`CGO_ENABLED=0` remains an invariant; `make dist` must stay green across the
release matrix.

### 2. The database is authoritative for the hub domain

Hub-owned state moves into the database and the legacy JSON files are
imported once, then deleted:

- **Registrations** — `repos.json` + `workspace.json` → tables. The
  fail-closed exposure rules of ADR 0003 §2 are unchanged; only the storage
  moves.
- **Queue** — `queue.json` → tables, making dedup/reorder/drain-policy
  updates transactional. Queue ownership is already hub-only (loop processes
  only write drain reports).
- **Issue store** — an `issues` table plus per-repo sync cursors. Every issue
  row carries a `source` binding (`internal` | `linear` | `jira`); synced and
  internal issues share one table, one backlog, one search index. Authority is
  phased:
  - **Internal issues** (`source=internal`) are authoritative in the database
    from day one: created and edited through the hub API/web UI, identified by
    a repo-scoped `ISSUE_PREFIX` + sequence, and runnable by the loop through
    an "internal" tracker provider backed by the hub API — so a repo needs no
    external tracker at all.
  - **Synced issues** are a read mirror in this phase. A hub background loop
    syncs each registered repo's Project: full pull first, then incremental by
    tracker `updatedAt` cursor, using server-side Project filtering (never the
    whole-team walk). The external tracker remains their source of truth;
    trau's writes to them (status transitions, labels, comments) keep going
    direct to the tracker API as today, and the next sync tick folds the
    change back into the store — eventually consistent with no conflict
    logic.
- **Search** — FTS5 virtual tables over the issue store (and later, derived
  run history).

Authoritative tables get real migrations: embedded, forward-only, versioned
SQL applied at open. They are expected to stay few and small.

### 3. Run artifacts stay files; the database holds a rebuildable projection

`events.jsonl`, checkpoint `state` files, `tokens.jsonl`, and pty transcripts
remain the durable, greppable source of truth for everything a loop produces —
the crash-forensics and tail-following properties are load-bearing (heartbeat
gap analysis, `FAILURE_REASON` inspection) and a torn JSONL line is skippable
where a torn database page is not. The checkpoint format stability invariant
(AGENTS.md) is unchanged.

The hub ingests these files into **derived tables** (`events`, `runs`,
`token_calls`) by tailing byte offsets — the mechanism the event SSE stream
already uses — and serves list, pagination, aggregation, and search queries
from them. Derived tables are versioned separately and are **never
migrated**: on schema mismatch (or deletion, or corruption) they are dropped
and rebuilt from the files. Deleting `trau.db` must never lose run history.

### 4. Explicitly out of scope

- **Write-back sync for external issues** (local-first: trau writing to the
  local row and syncing back to Linear/Jira). That is the conflict/echo/
  partial-failure swamp; it gets its own ADR when the issue store has proven
  itself. Same deferral for comment/attachment mirroring depth and
  webhook-driven sync.
- Loops writing to the database directly (rejected: worse failure blast
  radius mid-run, loss of file forensics, buys nothing the ingestion path
  doesn't).
- Per-repo database files (rejected: cross-repo search and one sync loop want
  one store; run artifacts still live with the repo).
- Ingesting pty transcripts into the database (they stay files; retention for
  `_agent-results` is a separate hygiene concern, no storage engine helps it).

## Consequences

- **Positive:** trau runs with no external tracker (internal issues,
  loop-runnable end to end); instant backlog paint from the issue store (the
  measured 2.5–6.6 s fetch leaves the request path); real `LIMIT/OFFSET`
  pagination and SQL aggregation for events/costs/runs; FTS5 search becomes a
  feature, not a project; hub JSON read-modify-rewrite code retires;
  transactional queue operations.
- **Negative / accepted:** ~+10 MB binary from the pure-Go driver; an
  ingestion/invalidation layer to maintain; two representations of run
  history (one mechanism — offset-tail ingestion — not per-feature sync);
  the SSE resume-cursor contract migrates from byte offsets to rowid cursors
  where endpoints move onto derived tables.
- ADR 0003 §1–2 (registration semantics, fail-closed exposure) stand. §3
  stands for run artifacts; for hub-owned state it is superseded by this ADR.
