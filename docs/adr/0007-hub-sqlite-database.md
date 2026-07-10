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
  search index. **The store is the working copy trau reads**: pick
  eligibility, prompt-building (title, description, comments), backlog, and
  search are all store reads through the hub API — the pipeline makes no
  tracker read calls at run time.
  - **Internal issues** (`source=internal`) exist only here — they are never
    pushed to any external system. Created and edited through the hub API/web
    UI, identified by a repo-scoped `ISSUE_PREFIX` + sequence, runnable by
    the loop — so a repo needs no external tracker at all. Tickets trau
    itself files (the verify loop's bug reports) become internal issues,
    never external ones.
  - **Synced issues: sync is one-way, inbound.** The external tracker owns
    issue content — title, description, comments, and metadata flow tracker →
    store only. Trau never edits content and never creates or deletes issues
    in the external tracker. Its only writes to a synced issue are
    operational — workflow status and settings (labels) — performed directly
    against the tracker API as today, with the store row updated in the same
    motion; the next sync pass confirms. No outbound sync engine and no
    conflict policy: content has a single writer.
  - **Lifecycle and reconciliation.** Incremental `updatedAt`-cursor pulls
    (server-side Project filtering — never the whole-team walk) catch edits
    to existing tickets, but deletions, archives, and moves out of the
    Project never appear in an updated-since query. A periodic
    reconciliation sweep therefore diffs the Project's full identifier set
    against the store: missing issues are tombstoned locally with defined
    cascades — dropped from the Queue and boards, run artifacts and
    checkpoints untouched, internal issues never affected. A force-resync
    drops a repo's synced rows and re-pulls clean (internal issues
    preserved). Pick nudges a sync first so a ticket finished, reopened, or
    deleted out-of-band cannot be picked from stale state; Run once resolves
    an ID from the store first and falls back to the tracker (fetch, sync
    in, then process from the store), subject to the Project ownership
    guard.
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

- **Outbound sync — permanently, by design, not deferred.** Trau does not
  edit synced-issue content, and does not create or delete issues in the
  external tracker; its tracker writes stay what they are today (status,
  labels). If a future need arises it requires a new ADR. Deferred (not
  rejected): attachment sync and webhook-driven (push) sync — polling
  cursors are enough at this scale.
- Loops writing to the database directly (rejected: worse failure blast
  radius mid-run, loss of file forensics, buys nothing the ingestion path
  doesn't).
- Per-repo database files (rejected: cross-repo search and one sync loop want
  one store; run artifacts still live with the repo).
- Ingesting pty transcripts into the database (they stay files; retention for
  `_agent-results` is a separate hygiene concern, no storage engine helps it).

## Consequences

- **Positive:** trau runs with no external tracker (internal issues,
  loop-runnable end to end); the pipeline's run-time tracker dependency
  shrinks to status/label writes — every read (pick, prompts, backlog,
  search) is local, so tracker latency and read rate limits leave the run
  path; instant backlog paint from the issue store (the measured 2.5–6.6 s
  fetch leaves the request path); real `LIMIT/OFFSET` pagination and SQL
  aggregation for events/costs/runs; FTS5 search becomes a feature, not a
  project; hub JSON read-modify-rewrite code retires; transactional queue
  operations.
- **Negative / accepted:** ~+10 MB binary from the pure-Go driver; an
  ingestion/invalidation layer to maintain; two representations of run
  history (one mechanism — offset-tail ingestion — not per-feature sync);
  the SSE resume-cursor contract migrates from byte offsets to rowid cursors
  where endpoints move onto derived tables; an inbound sync + reconciliation
  layer whose failure modes — stale-store reads, missed deletions between
  sweeps — are ours to own and surface; issue reads are only as fresh as the
  last sync, mitigated by sync-before-pick, stale-triggered refresh, and the
  reconciliation sweep.
- ADR 0003 §1–2 (registration semantics, fail-closed exposure) stand. §3
  stands for run artifacts; for hub-owned state it is superseded by this ADR.
