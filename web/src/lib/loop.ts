import type { Instance } from '@/lib/instances'
import { isActiveState, toSessionState } from '@/lib/overview'
import type { QueueItem, QueueResponse } from '@/lib/queue'
import type { Run } from '@/lib/runs'
import {
  buildTimeline,
  type Timeline,
  type TimelineTicket,
} from '@/lib/timeline'

export type LoopView = 'running' | 'builder'

// loopView picks which shape the Loop page renders. Draining always shows the
// running view, but a run can be in flight with the flag down — the drain
// disarms as its last item settles, and CLI starts never arm it — so an
// instance in an active session state that carries a ticket keeps the
// running view up until the run settles. Everything else gets the builder.
export function loopView(draining: boolean, instance?: Instance): LoopView {
  if (draining) return 'running'
  if (instance?.ticket && isActiveState(toSessionState(instance.session_state))) {
    return 'running'
  }
  return 'builder'
}

export type LoopHaltKind =
  'paused' | 'stopped' | 'budget' | 'fault' | 'quarantined'

export interface LoopHalt {
  kind: LoopHaltKind
  ticket: string
  reason: string
}

export interface LoopStateInput {
  queue?: QueueResponse
  runs: Run[]
  instance?: Instance
}

export interface LoopState {
  view: LoopView
  timeline: Timeline | null
  halt: LoopHalt | null
}

// repoInstance picks the one instance the Loop page speaks for. A takeover wins
// over anything else registered against the repo, so a human holding the ticket
// is the headline and the halt banner stays down behind them.
export function repoInstance(
  instances: Instance[],
  repo: string,
): Instance | undefined {
  const scoped = instances.filter((i) => i.repo === repo)
  return (
    scoped.find((i) => toSessionState(i.session_state) === 'takeover') ??
    scoped[0]
  )
}

export function isTakeover(instance?: Instance): boolean {
  return toSessionState(instance?.session_state ?? '') === 'takeover'
}

// A takeover is a human holding the ticket, not a stopped loop, so it counts as
// live alongside the states a drain runs in.
function instanceLive(instance?: Instance): boolean {
  if (!instance) return false
  const state = toSessionState(instance.session_state)
  return isActiveState(state) || state === 'takeover'
}

// executingTickets lists what the hub has already handed to a child in this
// queue snapshot. An epic lends its status to every sub-issue, so a resumed
// child counts from the moment the drain arms it.
function executingTickets(items: QueueItem[]): Set<string> {
  const ids = new Set<string>()
  for (const item of items) {
    if (item.status !== 'running') continue
    ids.add(item.id)
    for (const sub of item.sub_issues ?? []) ids.add(sub.id)
  }
  return ids
}

// haltFor names one settled ticket in the banner's vocabulary, matching the pill
// its row already shows: a give-up is a budget stop when the reason says so and
// a quarantine otherwise.
function haltFor(t: TimelineTicket): LoopHalt | null {
  const reason = t.reason ?? ''
  switch (t.status) {
    case 'paused':
      return { kind: 'paused', ticket: t.id, reason }
    case 'stopped':
      return { kind: 'stopped', ticket: t.id, reason }
    case 'failed':
      if (t.failureClass === 'gave_up') {
        return {
          kind: /budget/i.test(reason) ? 'budget' : 'quarantined',
          ticket: t.id,
          reason,
        }
      }
      return { kind: 'fault', ticket: t.id, reason }
    default:
      return null
  }
}

// currentHalt reports what has the loop stopped now, never what stopped it once.
// A live instance outranks everything — work is in flight. A queue entry the hub
// marked running outranks the checkpoint failure it is being resumed from, which
// closes the window between a resume and its child registering. Of what is left,
// only the newest settle reaches the banner: a clean one means the loop moved on.
// A paused epic is the last word: a declined finalize halts the epic row and no
// leaf, so the leaf scan finds nothing to name.
function currentHalt(
  timeline: Timeline,
  items: QueueItem[],
  instance?: Instance,
): LoopHalt | null {
  if (instanceLive(instance)) return null
  const executing = executingTickets(items)
  const settled = timeline.settled.filter((t) => !executing.has(t.id))
  const latest = settled[settled.length - 1]
  const halt = latest ? haltFor(latest) : null
  if (halt) return halt
  const stalled = items.find(
    (it) => it.kind === 'epic' && it.status === 'paused',
  )
  return stalled
    ? { kind: 'paused', ticket: stalled.id, reason: stalled.reason ?? '' }
    : null
}

// projectLoopState is the Loop page's current state in one pass: the view shape,
// the queue/run/instance join the card renders, and the halt the banner and tab
// title read.
export function projectLoopState({
  queue,
  runs,
  instance,
}: LoopStateInput): LoopState {
  const items = queue?.items ?? []
  const timeline = queue
    ? buildTimeline(items, runs, instance, queue.draining_since)
    : null
  return {
    view: loopView(queue?.draining ?? false, instance),
    timeline,
    halt: timeline ? currentHalt(timeline, items, instance) : null,
  }
}
