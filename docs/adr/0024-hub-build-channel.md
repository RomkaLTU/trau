# ADR 0024 — Build channel: the hub can rebuild a registered checkout and restart onto it

- **Status:** Accepted
- **Date:** 2026-07-29
- **Deciders:** Romas (sole maintainer)
- **Refs:** [COD-1330]; [ADR 0004](0004-hub-autostart.md) (the hub is a port-locked singleton); [ADR 0023](0023-platform-support-windows.md) §5 (`.exe` artifacts)

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
- **A failed build changes nothing.** The hub keeps serving, the channel stays
  `release`, and the last ~20 lines of build output are kept for the UI. The same
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

### 4. A launchd-supervised hub refuses

Under `trau hub supervise` (ADR 0022 §2) launchd owns the process and restarts it
from the binary its plist names, so a successor path the hub chose would be
discarded. The switch refuses with that reason rather than restarting onto the
release build it was asked to leave.

## Consequences

- The hub can execute a repo's build command on the host. That is a real
  escalation over what the API did before, and it is why the gate is registration
  plus an in-repo opt-in rather than the bearer token alone. Note that the
  command itself is repo-owned config: a repo that can set
  `HUB_RELOAD_BUILD_CMD` can already run anything, which is why `HUB_DEV_BINARY`
  needs no separate containment.
- Switching *back* to release is not in this slice. A dev hub is left on dev
  until a package-manager upgrade or a manual restart moves it; the honest way
  back is the release install's own path, not a second inverse endpoint.
- The switch is a restart, so it interrupts work exactly as the Restart button
  does, and the web UI reuses that button's confirmation and impact list rather
  than inventing a gentler-looking one.
- The SPA is embedded and its assets are not content-hashed, so a client that
  fires a switch must reload the page once the successor answers — the same
  full-reload the one-click update already does.

[COD-1330]: https://linear.app/codesomelabs/issue/COD-1330/hub-channel-switch-to-dev-rebuild-the-repo-tree-and-restart-the-hub
