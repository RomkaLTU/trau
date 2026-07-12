# ADR 0008 — Run data is DB-first through the hub

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-823 (epic COD-822)
- **Supersedes:** ADR 0007 §3 ("Run artifacts stay files; the database holds a
  rebuildable projection")
- **Amends:** ADR 0005 (presence *mechanism*, not its semantics)

## Context

ADR 0007 introduced the hub-owned SQLite database but drew a deliberate line at
§3: hub-domain state (registrations, queue, issues) became authoritative in the
database, while **run artifacts stayed files** — `events.jsonl`, checkpoint
`state`, `tokens.jsonl`, pty transcripts — with the database holding only a
rebuildable projection tailed from those files. Two properties were called
load-bearing and justified keeping the files:

1. **Forensics / greppability** — heartbeat-gap analysis and `FAILURE_REASON`
   inspection by grepping `events.jsonl`; the incident workflow that diagnosed
   the drain-fault and process-restart bugs was line-grep over the real files.
2. **Torn-write durability** — "a torn JSONL line is skippable where a torn
   database page is not."

Both premises have since weakened, and the file-first posture has cost us:

- The dual representation ADR 0007 accepted as a negative ("two representations
  of run history") is a standing tax: a 1-second file-tailing ingest loop with
  per-file byte-offset and size+mtime cursors (`internal/webserver/ingest.go`),
  reconstructing tables the hub could have owned outright.
- The run-file surface is large and layout-fragile. Where run files land is
  config-driven (`RUNS_DIR`, resolved against the repo root), and getting that
  resolution wrong produced a class of bugs — the drain hardcoded `.trau/runs`
  while the child wrote to a configured `runs/`, losing fault reports
  (COD-811/812/813). Every consumer that re-derives the runs path is a place
  that can disagree with the writer.
- The child↔hub write seam is already proven for the harder case. The internal
  tracker provider (ADR 0007) drives issues, status, labels, and comments
  through `internal/hubclient` over HTTP and **never opens the database**. Run
  data is the same seam, not a new risk.
- The forensics objection is now answerable by something better than grep. A
  forensics query CLI (slice COD-834) runs structured queries over
  events/checkpoints/tokens — time-ranged, per-phase, cross-run, joined — which
  is strictly more than line-grep gave. The torn-line concern is a non-issue for
  a WAL database: SQLite commits are atomic, so there is no half-written row to
  skip; a crashed write is rolled back on recovery, where a torn JSONL line is a
  real artifact you must defensively skip. The two load-bearing properties are
  preserved by different, better mechanisms — so §3's premise no longer holds.

There are zero external users, so this reversal needs no compatibility shims.

## Decision

Run data becomes **DB-first through the hub**. The loop child stops reading and
writing run files; it sends all run data to the hub over HTTP, and the hub — the
sole database writer — persists it. Files cease to be the source of truth for
anything a run produces.

### 1. The write path reverses: children send run data to the hub

Everything a run produces flows child → hub over the `hubclient` HTTP API:
checkpoints, events, token calls, cost anomalies, phase artifacts
(`handoff.md`, `rubric.json`, `verdict.json`, `buildnotes.md`, per-phase logs),
transcript chunks, lessons, presence heartbeats, and drain outcomes. Today
`hubclient` speaks only issues/tracker sync; this ADR mandates a run-write API
alongside it (tracer bullet COD-824).

The **single-writer invariant survives the reversal**: only the hub process
opens the databases for writing. The child never opens a database — exactly as
the internal tracker provider already behaves. The hub is a machine singleton
(ADR 0004), so this remains single-writer without cross-process locking. The one
narrow exception is `trau doctor`, which opens the database **read-only**
(`mode=ro`) for a health/integrity probe and writes nothing; the invariant is
precisely "the hub is the sole *writer*."

### 2. checkpoints, events, token_calls are promoted to authoritative

`checkpoints`, `events`, and `token_calls` move from **derived projections**
(tailed from files, versioned separately, never migrated, dropped-and-rebuilt on
mismatch) to **authoritative stores** with real embedded, forward-only,
versioned migrations — the same regime ADR 0007 §2 gave hub-domain tables.
Rebuild-from-files ceases to exist. The 1-second file-tailing ingest and its
byte-offset/size+mtime cursor bookkeeping are deleted at closeout (COD-833);
the SSE resume-cursor contract moves fully onto rowid cursors.

The **phase-ranking invariant is preserved, its substrate is not.** The semantic
ordering (`building → built → handed_off → verified → pr_open → merged`,
`quarantined = 9`) must stay stable across versions exactly as before — it is now
columns and rows the hub owns and migrates forward, rather than sanitized
`KEY=value` lines in a `state` file. AGENTS.md's "the file format and phase
ranking must stay stable" invariant narrows to the ranking; the file-format half
is superseded here (AGENTS.md to be updated at closeout).

### 3. Hub-down mid-run: retry, then pause blamelessly

The hub can vanish mid-run (crash, kill, upgrade). Autostart (ADR 0004) covers
*startup*; this covers *mid-run* outages. The contract, with **no local spool
files**:

- **In-memory buffer.** Unacknowledged run-data writes queue in the child's
  memory, bounded at a byte cap (**default 32 MB**, config
  `HUB_WRITE_BUFFER_BYTES`). Structured writes (checkpoints, events, token
  calls, heartbeats) are small and effectively never overflow; if the cap is
  reached before the window expires, the least-authoritative payloads
  (transcript chunks — retention-pruned anyway) are dropped oldest-first so a
  checkpoint or event is never evicted.
- **Bounded retry window.** The child retries the flush with exponential backoff
  for a total of **~30 s** (config `HUB_WRITE_RETRY_WINDOW`). This rides out a
  transient blip or a fast manual `trau serve` restart (a freed port rebinds in
  well under a second — ADR 0004 §2) without burning a run.
- **Then pause, blamelessly.** If the hub is still unreachable when the window
  expires, the run **pauses** — a `PausedError`, the same blameless class as the
  auth-stall pause: surfaced as "⏸ hub unreachable", WIP intact, resumable. It
  is not a fault; nobody is to blame for a downed local daemon.
- **The recovery substrate is the last hub-persisted checkpoint plus git.** WIP
  lives on the pushed feature branch (git, not hub-mediated), and the last
  checkpoint the hub persisted before it went down is the resume point. A rerun
  after the hub is back reconnects and replays at most the outage window's worth
  of incremental progress. Because the checkpoint writer *is* the hub, a run that
  pauses hub-down cannot record its own pause in the database — that is fine: the
  pause is shown to the user in-process, and the hub re-derives state from the
  last persisted checkpoint on rerun.

Child-driven hub *respawn* on mid-run death is explicitly **out of scope**: ADR
0004 §3 keeps headless children from autostarting on purpose, and reversing that
is a separate decision. Retry-then-pause is the whole contract.

### 4. Transcripts move to a separate `transcripts.db`

Pty transcripts are the largest byte source by far (~2 MB per build phase today,
truncate-and-rewrite per phase, **no pruning anywhere** — they accumulate
unbounded under `runs/_agent-results/`). Putting them in the authoritative
database would bloat the hot store and drag every backup/VACUUM. So they get
their own file:

- **`~/.trau/transcripts.db`** (under `TRAU_HOME`), hub-owned, WAL, opened by the
  hub only. Transcripts are stored as **chunked rows** keyed by
  `(run, phase/label, seq)` so appends are cheap and reads can range/paginate.
- **Live tail fans out from hub memory.** In-flight transcript chunks stream
  child → hub → an in-memory ring that feeds the SSE live tail, so the hot path
  never hits the database. Chunks are persisted to `transcripts.db` as they
  arrive; **replay** of a finished run serves from the database.
- **Mandatory retention.** Transcript rows are pruned by **run count**: the hub
  keeps the most recent **N completed runs per repo** (default **50**, config
  `TRANSCRIPT_RETENTION`) and prunes older runs' transcripts on serve startup and
  on a periodic timer, followed by incremental vacuum
  (`auto_vacuum = INCREMENTAL`) to reclaim space. In-flight runs are never
  pruned; a per-repo count keeps a busy repo from starving a quiet one. Because it
  is a separate file, vacuuming `transcripts.db` never blocks the authoritative
  store.
- **Wholesale delete is safe — by design the one file you may `rm`.**
  `transcripts.db` is the least-authoritative store; it holds only transcripts
  (ADR 0007 §5 already called their retention "a separate hygiene concern").
  Deleting it, or its being corrupt, loses replay for past runs but loses **no
  run history** (checkpoints/events/token calls live in `trau.db`) and breaks no
  live run (tail is served from hub memory). The hub recreates it empty on next
  open.

### 5. Durability and the no-downgrade stance

Once events/checkpoints/token calls are authoritative, `trau.db` is
**irreplaceable for run history** — there is no rebuild-from-files fallback.

- **Durability.** `trau.db` keeps the ADR 0007 pragmas: WAL, busy timeout,
  foreign keys on, `synchronous = NORMAL` (WAL-safe). The hub checkpoints the WAL
  on clean shutdown.
- **Backup guidance.** `trau doctor` gains a SQLite `integrity_check` over the
  databases. An occasional `VACUUM INTO` snapshot is the recommended backup — it
  is online and non-blocking — documented but not automated; proportionate to a
  single-user local tool. `transcripts.db` needs no backup (see §4).
- **No downgrade.** Once children write run data directly and the file-tailing
  ingest is deleted (COD-833), there is no file-era representation to fall back
  to. An older binary that expects run files will not find them (children stopped
  writing them) and cannot rebuild — run history is not *lost* (it is in
  `trau.db`), merely invisible to a binary that predates the schema. Forward-only,
  accepted (zero users). This is a stronger stance than ADR 0007 §4's
  empty-JSON-recreation downgrade, which applied to hub-domain state only.

### 6. Exemption list — what may legitimately stay on disk

After this epic, **any persistent file a run writes to disk is a bug**, with
these exemptions:

1. **Configuration** — `trau.ini`, `<repo>/.trau.ini`, `~/.trau.ini`, and env
   (`TRAU_*`, `RUNS_DIR`, `TRAU_HOME`, provider `*_CONFIG` keys). Read on every
   run; written by onboarding and the web Settings editor. Config is not run data.
2. **Repo-owned content** — skills (`.claude/skills`, `.kimi/skills`,
   `.agents/skills`), checks (`.trau/checks`), CI workflows, manifests,
   `skills-lock.json`. Owned by the target repo; trau only reads them.
3. **Provider-owned files** — `~/.claude` (`CLAUDE_CONFIG_DIR`), `~/.codex`,
   `~/.kimi-code` (sessions, `config.toml`, credentials). Owned by the provider
   CLIs; trau reads them for usage/probe and session stats.
4. **Agent-interface files** — the ephemeral child↔agent-CLI wire: `/tmp`
   handoff/verify/buildnotes/lesson payloads, the codex message temp, and the
   agent's `.result.json` result handoff (the CLI writes it, the loop reads it
   back). These are transport, not durable run data. The `.result.json` / `.size`
   companions currently land under `runs/_agent-results/`; the epic relocates
   them to the `/tmp` agent-interface convention (or classes them explicitly as
   exempt agent-interface files) so `runs/` is not treated as durable storage.
5. **Timelog** — `~/.trau/time/<repo>/<ticket>.json` (or the repo-scoped
   variant). Explicitly **out of scope** for this epic (a separate concern; see
   the planning-module removal, COD-810).
6. **The databases themselves** — `trau.db`, `transcripts.db`, and their
   `-wal`/`-shm` sidecars under `~/.trau`. These *are* the store.
7. **Ephemeral diagnostic probes** — `trau doctor`'s `.trau-doctor-write-test`
   (created and removed immediately). Not persistent state.

**Resolved by this ADR (moves off disk):**

- The instance registry `~/.trau/instances/<pid>.json` — the last per-PID file —
  moves to a hub heartbeat API (§7).
- `runs/.gitignore` and the append trau makes to the *target repo's* `.gitignore`
  exist only to keep run artifacts out of the repo. Once the child writes no
  durable artifacts under `runs/`, there is nothing to ignore; the closeout
  removes these writes (keeping a minimal ignore only if exempt agent-interface
  files still land there).
- The legacy runs-dir migration (`internal/state/migrate.go`) retires with the
  file era.

### 7. ADR 0005 amendment — presence keeps its semantics, changes its mechanism

ADR 0005's decision stands in full: **live activity is instance-reported, never
derived from run artifacts.** Only the *mechanism* changes.

- **Changed:** the registry entry and its heartbeat move from
  `~/.trau/instances/<pid>.json` files (glob-read by the hub, `os.Remove`-reaped
  on exit/staleness) to a **hub heartbeat HTTP API**, with entries held in hub
  memory (and/or a table). This removes the last per-PID file and the stale-file
  reaping.
- **Preserved:** instance-reported `session_state`
  (`idle | grazing | working | parked | stopping`); the boundary that an entry
  says only *that* a session parked, never why (the why stays on the checkpoint);
  and **pid-only liveness via `signal 0`** — the hub still probes the reported
  PID, and ADR 0005's rejection of heartbeat-staleness reaping (which would drop
  the repo-is-live guard) stands. Liveness is not "did the heartbeat arrive."
- **Interplay with §3:** when the hub is down an instance cannot heartbeat, but
  there are no live surfaces to serve then anyway; the hub re-derives liveness via
  `signal 0` on return. Consistent with retry-then-pause.

## Consequences

- **Positive:** one representation of run history — the dual-representation tax
  and the entire 1-second ingest/cursor machinery are deleted; the class of
  runs-dir layout/hardcode bugs (COD-811/812/813) disappears because there is one
  writer and no path re-derivation; SQL over run history (pagination,
  aggregation, FTS, cross-run forensics) is directly authoritative; the child's
  disk-write surface collapses to config plus ephemeral agent-interface temp
  files; `trau.db` becomes the single thing worth backing up.
- **Negative / accepted:** `trau.db` is now irreplaceable for run history, so
  backup discipline matters (mitigated by `doctor` integrity checks and
  `VACUUM INTO` snapshots); a mid-run hub outage is a new failure mode that
  pauses the run (blameless, git-recoverable); a run-write HTTP API to build and
  version (COD-824 onward); transcript retention can silently drop old
  transcripts (documented, by design); no downgrade (a binary predating the
  schema cannot read new-era run data).
- ADR 0007 §1–2 and §4–5 stand; §3 is superseded by this ADR. ADR 0005's
  decision stands; its file-based mechanism is superseded by §7. ADR 0003 §1–2
  (registration semantics, fail-closed exposure) and ADR 0004 (autostart) are
  untouched.
