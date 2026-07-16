# ADR 0015 — The Queue is the web's only start path

- **Status:** Accepted
- **Date:** 2026-07-16
- **Deciders:** Romas (sole maintainer)

## Context

ADR 0006 made the hub's per-repo drainer the queue executor, and it spawns a
queued ticket as exactly the child a manual web Run once spawns
(`--parent <id> --once`) — a queued run is indistinguishable from a manual one.
Yet the web kept four independent direct-spawn surfaces that bypass the queue:
the Run once page, Resume on the run page and on ledger rows, the overview
board's per-ticket launch, and the instances page's RunControls panel — all
POSTing `/api/v1/instances`. Two spawn paths meant two outcome bookkeepings
(the drain's reconcile vs `recordSpawnOutcome`) and an ambiguous interplay: a
direct run makes an armed drain silently wait. Only one run can hold a repo's
working tree at a time, so the distinction bought nothing.

## Decision

Web launching unifies on the Queue. The single gesture is **Run next**: the
ticket (or epic, taken whole) joins the front of the Queue and the drain arms —
immediate on an idle repo, after the live run otherwise; pending items behind
it still drain, and the confirm copy says so. Run next on an already-pending
item moves it to the front instead of erroring. Web Resume is Run next on a
ticket with a checkpoint — the child is unchanged and the run gains the drain's
outcome reconciliation. The Provider override moves onto the queue item and the
drain passes `--provider`. The Run once page's fetch-then-confirm machinery —
status warnings, the wrong-project block, the confirmless path for repos with
no tracker reader — moves into the Loop card's add flow rather than dying.

`POST /api/v1/instances` is deleted along with `StartRequest`,
`recordSpawnOutcome`, and the RunControls panel: the queue is the only way the
hub starts work, and spawn-death pinning is covered by the drain's
`classUnknown` reconcile. Dry-run stays (read-only). The `/run-once` route
redirects to `/loop`, the same retirement pattern as the old `/queue`.

## Consequences

- "Run once" survives as a TUI/CLI-only term (`trau <ID>`, the TUI screen);
  `--once`/`--parent` remain the plumbing the drainer spawns children with.
- The web loses "run just the next eligible sub-issue of this epic" (the old
  `max=1` descent). Deliberate: an Epic is a unit of remaining work everywhere;
  one-slice-at-a-time is Run next on the specific sub-issue, which epic flow
  still builds on the epic branch.
- "Run now" was rejected as the gesture name — it over-promises whenever a run
  is live. "Run next" is true in every case.
- No web surface launches the bare grazing loop (none did before either); if
  that is ever wanted it returns as a deliberate feature, not a leftover
  endpoint.
- Landing after Run next is the Loop timeline, which shows both the
  runs-immediately and waits-in-line cases honestly.
