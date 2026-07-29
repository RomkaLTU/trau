# ADR 0024 — Build channel: the hub can rebuild a registered checkout and restart onto it, and back

- **Status:** Accepted
- **Date:** 2026-07-29
- **Deciders:** Romas (sole maintainer)
- **Refs:** [COD-1330], [COD-1331], [COD-1332]; [ADR 0004](0004-hub-autostart.md) (the hub is a port-locked singleton); [ADR 0022](0022-crash-resilient-orchestration.md) §2 (the LaunchAgent); [ADR 0023](0023-platform-support-windows.md) §5 (`.exe` artifacts)

## Context

Working on trau itself means running two versions of it. The hub a developer's
machine actually uses is the released one — installed by Homebrew, autostarted on
the first interactive session, holding :8728 — while the code being changed lives
in a checkout whose `bin/trau` nobody is running. Moving the machine onto the
checkout's build is a terminal ritual: `make build`, then `trau hub restart`,
which only works because `hub restart` re-execs whatever binary the *running*
process resolves to. From a release install that command is a no-op: the
successor comes back on the release binary again.

ADR 0004's singleton is what makes this awkward. There is one hub, it owns the
port and the databases, and the SPA is compiled into it — so "run the dev build"
is never "start a second one", it is "replace the one that is running".

The post-merge self-reload (`/api/v1/hub/reload`) already restarts a hub onto a
repo's own build, but it solves a different problem: a merged run asking the hub
to pick up what it just shipped. It refuses unless the hub is *already* running
from inside that repo, which is precisely the case a developer on a Homebrew
install is not in.

## Decision

### 1. Channel is derived from the executable, never recorded

A hub runs on the **dev** channel when its executable sits inside a registered
repo root, and the **release** channel otherwise. Nothing is stored: the
executable path and the registered set are both already known, and a derived
answer cannot drift from reality the way a persisted flag can after a manual
`brew upgrade` or a binary moved on disk.

`GET /api/v1/update` carries `channel` and, on dev, `channelRepo` — the root of
the repo that owns the build.

### 2. The switch is a rebuild plus a restart, and nothing else

`POST /api/v1/hub/channel` runs the repo's own `HUB_RELOAD_BUILD_CMD` in its
root, resolves what that produced through `HUB_DEV_BINARY` (default `bin/trau`,
relative to the root; on Windows the `.exe` twin is tried too, per ADR 0023 §5),
probes it with `--version`, and only then arms the existing restart machinery
pointed at that path.

Three properties follow from doing it in that order:

- **The working tree is built as it stands.** No checkout, no pull, no discard of
  tracked changes. This is a developer asking for *their* tree, not the
  post-merge reload, which serves a different caller and keeps its own git
  handling.
- **A failed build changes nothing.** The hub keeps serving on the build it
  already had, and the last ~20 lines of build output are kept for the UI. The same
  is true of a binary that cannot print its version: a restart onto it would end
  the hub for good, so an unprovable build is never adopted.
- **The restart is the existing one.** The switch does not invent a shutdown
  path; it hands a successor path to `trau serve`'s respawn, which already
  replays the outgoing hub's `serve` flags. Everything that makes a restart safe
  — the drain, the single-successor guarantee, the port handover — is unchanged.

The response is a `202` ack. The rebuild outlives the request, so progress is
polled on `/api/v1/update` (`idle | building | restarting | failed`) rather than
streamed.

### 3. Trust model: registered, plus the repo's own opt-in

A switch may only target a repo that is **registered** with the hub and whose
layered config sets **`HUB_SELF_RELOAD=1`**. That is the same consent the
post-merge reload asks for, and it lives in the repo rather than in the request:
a caller cannot name a directory into eligibility.

On a **non-loopback bind** the switch additionally requires
**`SERVE_ALLOW_REGISTER=1`**, mirroring repo (un)registration. The reasoning is
the same in both places: a bearer token proves the caller reached the hub, not
that they should be able to make it execute a tree on the host. Loopback callers
already own the machine.

The `withinRoot` guard on `/api/v1/hub/reload` — that the hub already runs from
the asking repo — is deliberately *not* part of this. It is that endpoint's whole
point and stays there; requiring it here would refuse exactly the release-install
case this ADR exists for.

### 4. The way back is the install on PATH, and it needs no repo

`{"channel": "release"}` restarts the hub onto the first `trau` **on PATH that no
registered repo root contains**. `exec.LookPath` stops at the first hit, which on
a machine whose checkout is on PATH is the dev build itself, so the search
continues past every entry a repo owns — restarting onto one of those would not
leave the dev channel at all. The candidate is probed with `--version` before it
is adopted, exactly as a fresh build is, and a machine with no release install is
refused with that reason rather than restarted.

Nothing is built, so no repo consents to this direction and `HUB_SELF_RELOAD`
does not apply: no repo provides the binary. The bind gate stays — the hub still
re-execs a path the caller did not name.

Asking for `dev` from a hub already on `dev` is not a no-op either: the tree is
built as it stands, so it is the rebuild-and-restart a developer runs as `make
reset`, available from the UI once the work was done interactively.

### 5. A newer release is not an update for a dev hub

The update check weighs the newest release against the version on disk, which
for a working-tree build is a `git describe` of whatever is checked out. That
comparison means nothing, so `/api/v1/update` reports `updateAvailable: false`
while the channel is `dev` and the UI says the release applies after switching
back. The checker itself is unchanged: a release-channel hub sees exactly what it
saw before.

### 6. A launchd-supervised hub takes its agent with it

Under `trau hub supervise` (ADR 0022 §2) launchd owns the process: a supervised
hub restarts by exiting, and KeepAlive brings the successor up from the binary
the plist names rather than from any path the outgoing hub chose. So the switch
rewrites the plist to the chosen build *before* it arms the restart — the
`TRAU_SUPERVISED=1` marker, the captured `PATH`/`TRAU_HOME` and the `hub.log`
redirects come across exactly as `trau hub supervise` writes them — and then lets
the ordinary supervised exit happen.

The rewritten file is not enough on its own: launchd respawns the job it
bootstrapped, not what is on disk. The agent is handed the new plist (`bootout`
then `bootstrap`) by a short-lived detached process, because a job cannot boot
itself out — `launchctl` waits for it to exit, so a `bootstrap` issued from the
hub would never be reached. That sequence starts while the hub is still draining,
which is what keeps KeepAlive from respawning the outgoing build in the gap: one
hub hands over to one hub, and the port is never left to nobody.

A plist that cannot be written aborts the switch before anything restarts. A
supervised hub re-execed onto a path its agent does not name is precisely the
silent snap-back this section exists to prevent, so the hub keeps serving and the
reason surfaces on `channelSwitch` the way a failed build does. `trau doctor`
reports the installed `ProgramArguments`, so an agent that did drift stays
visible.

## Consequences

- The hub can execute a repo's build command on the host. That is a real
  escalation over what the API did before, and it is why the gate is registration
  plus an in-repo opt-in rather than the bearer token alone. Note that the
  command itself is repo-owned config: a repo that can set
  `HUB_RELOAD_BUILD_CMD` can already run anything, which is why `HUB_DEV_BINARY`
  needs no separate containment.
- The release direction is only as good as PATH. A machine whose only trau is the
  checkout has nothing to go back to, and the UI disables the action with that
  reason rather than pretending otherwise.
- The switch is a restart, so it interrupts work exactly as the Restart button
  does, and the web UI reuses that button's confirmation and impact list rather
  than inventing a gentler-looking one.
- A supervised switch is the one place trau replaces its own LaunchAgent while
  running, and the bootout/bootstrap has to outlive the process doing it. If that
  detached spawn fails the hub still exits and KeepAlive brings the outgoing
  build back — the safe end of that failure, and the one `trau doctor` names.
- The SPA is embedded and its assets are not content-hashed, so a client that
  fires a switch must reload the page once the successor answers — the same
  full-reload the one-click update already does.

[COD-1330]: https://linear.app/codesomelabs/issue/COD-1330/hub-channel-switch-to-dev-rebuild-the-repo-tree-and-restart-the-hub
[COD-1331]: https://linear.app/codesomelabs/issue/COD-1331/switch-back-to-release-channel-manual-rebuild-and-restart-action
[COD-1332]: https://linear.app/codesomelabs/issue/COD-1332/channel-switching-under-launchd-supervision-rewrite-the-launchagent-to
