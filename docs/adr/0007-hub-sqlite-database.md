# ADR 0007 — Hub SQLite database: authoritative for hub state, derived for run artifacts

- **Status:** Accepted
- **Date:** 2026-07-10
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-770 (epic; slices COD-771…COD-785)
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
- **Issue store** — an `issues` table (plus comments and per-repo sync
  cursors). Every issue row carries a `source` binding (`internal` | `linear`
  | `jira`); synced and internal issues share one table, one backlog, one
  search index. **The store is the single working copy trau processes from**:
  pick, prompt-building (title, description, comments), status transitions,
  labels, and trau-written comments all go against the store through the hub
  API — the pipeline makes no tracker calls at run time.
  - **Internal issues** (`source=internal`) exist only here: created and
    edited through the hub API/web UI, identified by a repo-scoped
    `ISSUE_PREFIX` + sequence, runnable by the loop — so a repo needs no
    external tracker at all.
  - **Synced issues** converge with their external tracker in both
    directions. Inbound: a hub background loop syncs each registered repo's
    Project — full pull first, then incremental by tracker `updatedAt`
    cursor, server-side Project filtering (never the whole-team walk) —
    including issue metadata and comments. Outbound: trau's writes land in
    the store and push to the tracker write-through, backed by a
    pending-changes outbox so a tracker outage queues the push (the run
    continues) instead of stopping the loop. Comments are append-only both
    ways and never conflict; fields resolve last-write-wins by timestamp with
    anomalies logged. Run once resolves an ID from the store first and falls
    back to the tracker (fetch, sync in, then process from the store),
    subject to the Project ownership guard. Because pick's done-guards read
    the store, pick nudges a sync first so stale state cannot re-pick a
    finished ticket.
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

- **Merge sophistication beyond last-write-wins** (field-level three-way
  merge, CRDTs, offline multi-writer reconciliation). v1 conflict policy is
  deliberately simple: append-only comments, last-write-wins fields, logged
  anomalies. Also deferred: attachment sync and webhook-driven (push) sync —
  polling cursors are enough at this scale.
- Loops writing to the database directly (rejected: worse failure blast
  radius mid-run, loss of file forensics, buys nothing the ingestion path
  doesn't).
- Per-repo database files (rejected: cross-repo search and one sync loop want
  one store; run artifacts still live with the repo).
- Ingesting pty transcripts into the database (they stay files; retention for
  `_agent-results` is a separate hygiene concern, no storage engine helps it).

## Consequences

- **Positive:** trau runs with no external tracker (internal issues,
  loop-runnable end to end); the pipeline loses its runtime tracker
  dependency — a tracker outage or rate limit no longer stops a run (writes
  queue in the outbox and drain later), and tracker-caused pauses largely
  disappear by construction; instant backlog paint from the issue store (the
  measured 2.5–6.6 s fetch leaves the request path); real `LIMIT/OFFSET`
  pagination and SQL aggregation for events/costs/runs; FTS5 search becomes a
  feature, not a project; hub JSON read-modify-rewrite code retires;
  transactional queue operations.
- **Negative / accepted:** ~+10 MB binary from the pure-Go driver; an
  ingestion/invalidation layer to maintain; two representations of run
  history (one mechanism — offset-tail ingestion — not per-feature sync);
  the SSE resume-cursor contract migrates from byte offsets to rowid cursors
  where endpoints move onto derived tables; a sync engine (inbound cursor +
  outbound outbox) whose failure modes — echo loops, stale-store picks,
  stuck outbox rows — are ours to own and surface; issue processing is only
  as fresh as the last sync, mitigated by sync-before-pick and
  stale-triggered refresh.
- ADR 0003 §1–2 (registration semantics, fail-closed exposure) stand. §3
  stands for run artifacts; for hub-owned state it is superseded by this ADR.
