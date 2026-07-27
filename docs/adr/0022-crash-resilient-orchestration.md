# ADR 0022 — Surviving a hub outage: reconcile on boot, launchd supervision, bounded blameless re-attempts

**Status:** Accepted

## Context

A killed hub is a manual, multi-step recovery today. The COD-1151/COD-1158
post-mortem showed all three costs at once: the hub died mid-run, nothing brought
it back, and the queue item it had been draining stayed parked behind work that
had in fact already shipped — so the eventual recovery was "restart the hub, then
press Start per repo, so the loop can re-discover a fact the hub could have read
off disk".

Each cost has a different cause:

- **Stale queue items.** A parked item records the class it stopped with, not the
  state of the world. Work finished out of band — a human merging the PR, a child
  that died between its merge and its checkpoint write — leaves the item parked
  forever, and ADR 0008's single-writer rule means only the hub can settle it.
- **Nothing restarts the hub.** Autostart fires on the next *interactive*
  invocation (ADR 0004), and pipeline children deliberately never resurrect the
  hub they report to (ADR 0008, single-writer). Between those two, an unattended
  machine whose hub is killed has no orchestrator until a human logs in — and the
  23:13 `context deadline exceeded` in `~/.trau/hub.log` shows the on-demand path
  is least reliable exactly when it is needed.
- **Blameless pauses need a human click.** A provider rate wall and an
  unreachable hub both clear on their own, but the queue treats them like a fault:
  parked until someone presses Start.

## Decision

### 1. The reconcile sweep compares against ground truth, not the recorded class

On hub boot and on every drain arm, each **paused or failed** queue item is
compared against three independent proofs that its work already shipped:

1. its checkpoint reached `merged` in the authoritative table;
2. the hub's issue store — its mirror of the tracker — files it under `done`;
3. the PR its checkpoint recorded reads `MERGED` on the forge.

They are independent on purpose. A child SIGKILLed after its merge landed leaves
only the PR behind; work a human finished while the hub was down leaves only the
tracker status. Any one proof settles the item **done** through the same path a
finished child settles through, so sub-issues and the web queue follow, and emits
a `queue_reconciled` event naming which proof it was — a settle with no run behind
it must be explainable in the feed.

The three are ordered cheapest-first (two local database reads, then a `gh`
subprocess). Anything short of proof — including an item whose checkpoint records
a fault — is left exactly as it was, and the sweep never spawns anything. That is
what makes it policy-free: it only ever acknowledges completed work.

### 2. Supervision is launchd's job, and strictly opt-in

`trau hub supervise` installs a per-user LaunchAgent with `KeepAlive` and
`RunAtLoad`, so a crashed or killed hub is back in seconds. It is opt-in and
never implicit: handing a machine's hub to launchd is the user's decision, not a
side effect of running a loop.

Two consequences follow from launchd owning the process:

- The plist sets `TRAU_SUPERVISED=1`. The hub's self-restart (`/hub/restart`)
  normally spawns its own successor; under supervision it just exits, because
  `KeepAlive` is already starting one and two successors would race for the port.
- `trau hub restart` starts a supervised hub with `launchctl kickstart` rather
  than spawning a detached child, for the same reason. `trau hub supervise` on a
  machine that already has an unsupervised hub stops it first, behind the same
  guards a forced restart uses (refuses inside a managed run, refuses while loops
  are live), so launchd's copy is the one that binds the port. A machine that is
  already supervised takes the other path: the agent in place is booted out before
  the port is looked at — signalling a hub `KeepAlive` owns only makes launchd
  re-bind it — so the re-run that adopts a moved binary displaces nothing but a
  foreign process.

launchd gives an agent almost no environment, so the installing shell's `PATH` is
captured into the plist: every loop the hub spawns resolves `git`, `gh` and the
provider CLIs through it.

Rejected: making autostart install the agent (supervision would then be a side
effect of a normal run), and a trau-side watchdog process (a supervisor that can
itself be killed just moves the problem).

### 3. Automatic re-attempts are opt-in, blameless-only, and bounded

With `QUEUE_AUTO_RESUME=1`, an item the drain parked with the **blameless** class
(`state.FailPaused` — a provider rate/auth wall, a hub the child could not reach)
is re-armed once a backoff passes, up to `QUEUE_AUTO_RESUME_TRIES` times; then it
parks for a human exactly as it does today. A `queue_auto_resumed` event records
each re-attempt, since nobody clicked Start for it.

The re-attempt resumes the queue where it stands (`Queue.Rearm`) rather than
replaying the arm the run started with: a run armed with skip-resume resets every
unsettled item to pending, and replaying that on a re-attempt would return
already-shipped items to pending and run them again.

The class line is the whole policy. A fault, an unclassifiable outcome, and a
deliberate Stop are all excluded: the first two are not known to clear on their
own, and the third is a human's decision the hub must not overrule.

Default off, because a re-attempt spends tokens with nobody watching. The plan
lives in the hub's memory rather than in the queue schema — a hub that dies with
one outstanding comes back to the item parked, which is where a human would have
found it with the opt-in off anyway.

## Consequences

- A hub outage stops being a recovery procedure: the queue self-heals what
  verifiably finished, launchd brings the process back, and a blameless wall can
  clear itself when a repo opted in.
- The sweep can settle an item on a tracker status the hub last synced, so a
  stale mirror settles late rather than wrongly — it never settles an item the
  mirror shows open.
- Supervision is macOS-only. Other platforms keep ADR 0004's autostart, and
  `trau doctor` says which state the machine is in.
