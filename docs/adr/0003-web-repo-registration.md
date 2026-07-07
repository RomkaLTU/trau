# ADR 0003 — Web repo registration: hub-owned store, fail-closed when exposed

- **Status:** Accepted
- **Date:** 2026-07-06
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-751

## Context

COD-751 wants the whole loop drivable from Trau Web — the terminal is needed
only for the initial `trau serve`. Today a repo becomes startable from the hub
only through `SERVE_WORKSPACE`, which requires editing a config file and
restarting serve (the allowlist is captured once at `webserver.New`). A repo
the hub merely discovers through the instance registry stays observe-only.

`SERVE_WORKSPACE` is not just convenience config — it is the fail-closed
defense that stops a leaked bearer token from starting AI-agent subprocesses
in arbitrary paths on the server (see the serve exposure invariant in
AGENTS.md). Any web-based registration mechanism weakens that boundary unless
it is gated deliberately.

The ticket also floated moving hub state into SQLite because "the file system
files are getting bigger".

## Decision

### 1. Registered repos live in a hub-owned file, not in config

`POST /api/v1/repos` registers a repo by absolute path; `DELETE
/api/v1/repos/{repo}` unregisters it. Registrations persist in a hub-owned
JSON file under the trau home (`~/.trau/workspace.json`), read per-request and
merged with the static `SERVE_WORKSPACE` seed to form the effective allowlist.

- Not written into `.trau.ini`: config is loaded once at serve startup, so a
  config write would require a restart — defeating the ticket's goal.
- Validation at registration: path exists, is a directory, and is a git
  toplevel (`.git` present as dir or file, covering worktrees); normalized to
  an absolute path.
- Unregistering only revokes startability. The repo drops back to
  observe-only; run artifacts and registry history are untouched, consistent
  with how repos already linger after their loop exits.

### 2. Registration is fail-closed on exposed binds

- **Loopback bind:** registration is open — the caller already owns the
  machine, and the API is tokenless there by design.
- **Non-loopback bind:** registration is refused unless `SERVE_ALLOW_REGISTER=true`
  is set *in addition to* the mandatory `SERVE_TOKEN`. Default is closed.

Trade-off accepted: a remote-first user must set one extra config key once.
In exchange, a leaked token on an exposed hub still cannot widen the set of
directories trau will run agents in.

### 3. No SQLite — the filesystem remains the source of truth

Registration adds one small JSON file, the same scale as the existing
`~/.trau/repos.json`. The hub stays file-first: it reads the same run files
whether a loop is live or dead, `events.jsonl` remains the durable source of
truth, and the checkpoint format stays stable across versions. A database
would be a second source of truth to keep in sync and adds weight to the
static binary for no query need we have today. If JSONL read performance ever
becomes a measured problem, that is its own ticket — and any store introduced
then must be a rebuildable cache, never the source of truth.

## Consequences

- **Positive:** repos become startable from the browser without touching the
  server shell; the exposure invariant survives intact; no new storage engine.
- **Negative / follow-ups:**
  - Two sources feed the effective allowlist (`SERVE_WORKSPACE` +
    `workspace.json`); the repos API must report which one granted access so
    users understand why a repo is or isn't startable.
  - `SERVE_ALLOW_REGISTER` is new user-facing surface: document it in
    `trau.ini.example` and enforce it in `CheckExposure`-adjacent validation.
  - The web UI gains a global active-Repo context in the app shell (replacing
    per-page pickers) — a UI restructure tracked under COD-751, not a
    hub-architecture decision.
