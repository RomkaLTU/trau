# ADR 0004 — Hub autostart: first TUI session spawns a persistent, singleton daemon

- **Status:** Accepted
- **Date:** 2026-07-09
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-761

## Context

The Hub (`trau serve` — the JSON API + embedded web UI) is useful for every
run, but today it only exists if the user remembers to start it in a separate
terminal. COD-761 wants it to come up on its own: the web UI should be running
whenever someone is working with trau, with **exactly one Hub per machine** even
though many loops (CLI runs) may be live at once.

The constraints that shaped the decision:

- The Hub is designed to outlive any single loop — it reads run artifacts whether
  a loop is live or dead, and repos "outlive the loop" (ADR 0003). An in-process
  Hub tied to one loop would vanish mid-review the moment that loop exits.
- Exposure is fail-closed (AGENTS.md serve invariant): a non-loopback bind
  requires `SERVE_TOKEN`. Any implicit start must not weaken that.
- Registration (`registry.Register`) already marks the "this is a real run"
  boundary; read-only commands (`--status`, `--clear`, `--dry-run`, `doctor`,
  `watch`) return before it.
- The maintainer ships releases frequently, so a daemon from an older version
  will routinely still be holding the port after an upgrade.

## Decision

### 1. Detached daemon, not in-process

The first qualifying session spawns `trau serve` as a **detached child**
(`Setpgid`, own process group — the same pattern the Hub already uses to spawn
loops in `supervisor.go`), so the Hub outlives the loop that started it. An
in-process goroutine or an ownership-handoff scheme were rejected: the first
dies too early, the second adds leader-election complexity for no gain.

### 2. The port bind is the singleton lock

A starting session probes `GET /api/v1/health`. A healthy trau response → the
Hub is up, do nothing. Otherwise it spawns the detached `trau serve`, whose
`net.Listen` on `:8728` is the atomic arbiter: if two sessions race, the kernel
lets exactly one bind and the loser's child exits on `EADDRINUSE`. No PID file
or lock file — those reintroduce the stale-entry reaping the instance registry
already has to fight, and can desync from the real port state. The port is
self-cleaning: when the daemon dies the port frees immediately.

### 3. Trigger: interactive TUI sessions only

Autostart fires only from `runSession` (the interactive menu shell), not from
headless/`--once`/Run-once paths. This keeps a hub-spawned `--no-tui` loop child
from probing for a Hub at all, and keeps CI/headless runs quiet. Consequence: a
machine that has *only ever* run headless has no Hub until a TUI session or an
explicit `trau serve`; once any TUI session brings the daemon up it persists and
shows every loop from the registry, headless ones included.

### 4. Default on, inherits serve config, fail-closed by reuse

Autostart defaults on. `SERVE_AUTOSTART=0` (layered config) disables it;
`--no-serve` disables it for one run. The spawned daemon resolves the **same**
layered `SERVE_*` config an explicit `trau serve` would, so an autostarted Hub is
byte-identical to a hand-started one and the two never conflict. Because it runs
the existing `CheckExposure`, a non-loopback bind without a token simply can't
start — and since autostart is best-effort, it skips silently (with a one-line
stderr hint) rather than failing the loop. Pinning autostart to loopback was
rejected: it would silently ignore a deliberate remote user's `SERVE_BIND` and
then collide on the port with their later explicit serve.

### 5. Persist until reboot or explicit stop

The daemon runs indefinitely once up. Idle-shutdown and loop-ref-counting were
rejected because both tear the dashboard down at the worst moment — right after a
run finishes and the user wants to inspect its costs and transcripts. The
explicit-stop UX (`trau serve --stop` / a Hub PID in `/health`) is a deliberate
follow-up, out of scope here.

### 6. Auto-open the browser, health-gated and separately suppressible

On a **fresh** start (only when this session actually spawned the daemon, not
when it reused one), open the default browser after polling `/health` to 200 so
the page never races the bind. The open is best-effort and cross-platform
(`open`/`xdg-open`/`start`); failure (headless, SSH, no `$DISPLAY`) is swallowed.
`SERVE_OPEN=0` suppresses just the browser launch while keeping the daemon, and
the TUI always shows `Web UI: http://…:8728` as the fallback pointer.

### 7. Reuse any running Hub regardless of version

If `/health` responds as a trau Hub, reuse it even if its version differs from
the current binary — the run-file format is a stable cross-version invariant, so
an old Hub reading new runs is safe. A version mismatch only prints a one-line
notice suggesting a restart. Killing/restarting a possibly-in-use daemon is left
to the (deferred) stop UX, not done implicitly here.

## Consequences

- **Positive:** the web UI is present by default with no extra command; the
  exposure invariant survives untouched; one Hub serves all loops; no new lock
  file or storage.
- **Negative / follow-ups:**
  - A persistent detached daemon has no built-in stop; document `kill`/reboot
    until the `trau serve --stop` follow-up lands.
  - `SERVE_AUTOSTART` and `SERVE_OPEN` are new user-facing keys — document both
    in `trau.ini.example`.
  - Headless-only machines get no Hub without an explicit `trau serve`; called
    out in §3 by design.
  - A non-trau process squatting on `:8728` makes autostart skip with a hint;
    the user resolves it via `SERVE_PORT` or `SERVE_AUTOSTART=0`.
