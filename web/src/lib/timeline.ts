import type { RunState } from '@/components/trau/status-pill'
import type { Instance } from './instances'
import { isActiveState, toSessionState } from './overview'
import { queueTerminal, type QueueItem } from './queue'
import type { FailureClass, PRStatus, Run } from './runs'
import { stepPill } from './steps'

export type TicketStatus =
  'done' | 'running' | 'paused' | 'stopped' | 'failed' | 'skipped' | 'pending'

export interface TimelineTicket {
  id: string
  title: string
  status: TicketStatus
  source?: string
  provider?: string
  epicId?: string
  failureClass?: FailureClass
  reason?: string
  phase?: string
  prStatus?: PRStatus
  activity?: string
  detail?: string
  hasRun: boolean
  completedAt?: string
}

export type PendingEntry =
  | { kind: 'ticket'; ticket: TimelineTicket }
  | {
      kind: 'epic'
      id: string
      title: string
      source?: string
      done: number
      total: number
      children: TimelineTicket[]
    }

// Timeline is the client-side join of a draining queue's snapshot with its live
// run records: settled tickets in the order they actually completed, the one
// running ticket, and the remaining set in snapshot order. Epic group headers do
// not count toward done/total — only leaf tickets do.
export interface Timeline {
  total: number
  done: number
  settled: TimelineTicket[]
  running?: TimelineTicket
  pending: PendingEntry[]
  elapsedAnchor?: string
}

interface Leaf {
  id: string
  title: string
  snapshotState: string
  source?: string
  provider?: string
  epicId?: string
  reason?: string
}

// An epic's sub-issues share its binding: an internal epic is only ever filed
// with internal children, so they inherit its source rather than carrying one.
function flatten(items: QueueItem[]): Leaf[] {
  const leaves: Leaf[] = []
  for (const item of items) {
    if (item.kind === 'epic') {
      for (const sub of item.sub_issues ?? []) {
        leaves.push({
          id: sub.id,
          title: sub.title,
          snapshotState: sub.state,
          source: item.source,
          provider: item.provider,
          epicId: item.id,
        })
      }
      continue
    }
    leaves.push({
      id: item.id,
      title: item.title ?? '',
      snapshotState: item.status,
      source: item.source,
      provider: item.provider,
      reason: item.reason,
    })
  }
  return leaves
}

function resolve(
  leaf: Leaf,
  run: Run | undefined,
  instance?: Instance,
): TimelineTicket {
  const base = {
    id: leaf.id,
    title: leaf.title,
    source: leaf.source,
    provider: leaf.provider,
    epicId: leaf.epicId,
    hasRun: run !== undefined,
  }
  const isCurrent = instance?.ticket === leaf.id
  const working = isCurrent && instance?.session_state === 'working'
  const activity = working ? instance?.activity : undefined
  const detail = working ? instance?.detail : undefined

  if (run) {
    if (run.failure_class === 'paused') {
      return {
        ...base,
        status: 'paused',
        failureClass: 'paused',
        reason: run.failure_reason,
        phase: run.phase,
        completedAt: run.updated_at,
      }
    }
    if (run.failure_class === 'stopped') {
      return {
        ...base,
        status: 'stopped',
        failureClass: 'stopped',
        reason: run.failure_reason,
        phase: run.phase,
        completedAt: run.updated_at,
      }
    }
    if (run.failure_class === 'faulted' || run.failure_class === 'gave_up') {
      return {
        ...base,
        status: 'failed',
        failureClass: run.failure_class,
        reason: run.failure_reason,
        phase: run.phase,
        completedAt: run.updated_at,
      }
    }
    if (run.terminal) {
      return {
        ...base,
        status: 'done',
        phase: run.phase,
        completedAt: run.updated_at,
      }
    }
    return {
      ...base,
      status: 'running',
      phase: isCurrent && instance?.phase ? instance.phase : run.phase,
      prStatus: run.pr_status,
      activity,
      detail,
    }
  }

  if (isCurrent)
    return {
      ...base,
      status: 'running',
      phase: instance?.phase,
      activity,
      detail,
    }

  switch (leaf.snapshotState) {
    case 'done':
    case 'merged':
      return { ...base, status: 'done' }
    case 'failed':
    case 'faulted':
      return { ...base, status: 'failed', reason: leaf.reason }
    case 'skipped':
      return { ...base, status: 'skipped', reason: leaf.reason }
    case 'paused':
      return { ...base, status: 'paused', reason: leaf.reason }
    case 'running':
      return { ...base, status: 'running' }
    default:
      return { ...base, status: 'pending' }
  }
}

function isSettled(status: TicketStatus): boolean {
  return (
    status === 'done' ||
    status === 'failed' ||
    status === 'skipped' ||
    status === 'paused' ||
    status === 'stopped'
  )
}

export function buildTimeline(
  items: QueueItem[],
  runs: Run[],
  instance?: Instance,
): Timeline {
  const byTicket = new Map(runs.map((r) => [r.ticket, r]))
  const leaves = flatten(items)
  // A run can outlive its queue entry or never have one (a CLI start): an
  // instance ticket missing from the snapshot still joins as a leaf, whether the
  // instance is working it or parked on the halt it stopped at.
  const session = toSessionState(instance?.session_state ?? '')
  if (
    instance?.ticket &&
    (isActiveState(session) || session === 'parked') &&
    !leaves.some((l) => l.id === instance.ticket)
  ) {
    leaves.push({
      id: instance.ticket,
      title: byTicket.get(instance.ticket)?.title ?? '',
      snapshotState: 'running',
    })
  }
  const tickets = leaves.map((leaf) =>
    resolve(leaf, byTicket.get(leaf.id), instance),
  )
  const byId = new Map(tickets.map((t) => [t.id, t]))

  // Settle order comes from the runs' completion stamps. A leaf the queue
  // settled without a run has none of its own — it settled no earlier than the
  // leaf ahead of it, so it borrows that stamp and the stable sort keeps it
  // there instead of collapsing to the front.
  const stamped: { ticket: TimelineTicket; at: string }[] = []
  let at = ''
  for (const t of tickets) {
    if (!isSettled(t.status)) continue
    at = t.completedAt ?? at
    stamped.push({ ticket: t, at })
  }
  const settled = stamped
    .sort((a, b) => a.at.localeCompare(b.at))
    .map((s) => s.ticket)

  const running =
    tickets.find((t) => t.status === 'running' && t.id === instance?.ticket) ??
    tickets.find((t) => t.status === 'running')

  const remains = (t: TimelineTicket | undefined): t is TimelineTicket =>
    t !== undefined && !isSettled(t.status) && t !== running

  const pending: PendingEntry[] = []
  for (const item of items) {
    if (item.kind === 'epic') {
      const subs = item.sub_issues ?? []
      const children = subs.map((s) => byId.get(s.id)).filter(remains)
      if (children.length > 0) {
        const done = subs.filter(
          (s) => byId.get(s.id)?.status === 'done',
        ).length
        pending.push({
          kind: 'epic',
          id: item.id,
          title: item.title ?? '',
          source: item.source,
          done,
          total: subs.length,
          children,
        })
      }
      continue
    }
    const t = byId.get(item.id)
    if (remains(t)) pending.push({ kind: 'ticket', ticket: t })
  }

  const leafIds = new Set(leaves.map((l) => l.id))
  let elapsedAnchor = instance?.started_at
  for (const r of runs) {
    if (!leafIds.has(r.ticket) || !r.updated_at) continue
    if (!elapsedAnchor || r.updated_at < elapsedAnchor)
      elapsedAnchor = r.updated_at
  }

  return {
    total: tickets.length,
    done: tickets.filter((t) => t.status === 'done').length,
    settled,
    running,
    pending,
    elapsedAnchor,
  }
}

export interface BuilderView {
  queue: QueueItem[]
  settled: TimelineTicket[]
}

// itemSettled reports whether a builder queue item has no work left for a Start:
// a standalone ticket in a terminal state, or an epic whose sub-issues are all
// done. Paused stays actionable — a Start resumes it. An epic with no sub-issues
// stays actionable so it never silently vanishes into the Finished card.
function itemSettled(item: QueueItem): boolean {
  if (item.kind === 'epic') {
    const subs = item.sub_issues ?? []
    return subs.length > 0 && subs.every((s) => s.state === 'done')
  }
  return queueTerminal(item.status)
}

// builderView splits a non-draining queue for the idle builder: the items still
// worth reordering in the editable list, and the settled leaves collapsed into
// the Finished card. The settled side reuses buildTimeline so its rows, ordering,
// and tally match the running view exactly.
export function builderView(items: QueueItem[], runs: Run[]): BuilderView {
  return {
    queue: items.filter((it) => !itemSettled(it)),
    settled: buildTimeline(items.filter(itemSettled), runs).settled,
  }
}

export const FINISHED_PAGE_SIZE = 10

export interface FinishedState {
  expanded: boolean
  visible: number
}

export const FINISHED_INITIAL: FinishedState = {
  expanded: false,
  visible: FINISHED_PAGE_SIZE,
}

export type FinishedAction = { type: 'toggle' } | { type: 'more' }

export function finishedReducer(
  state: FinishedState,
  action: FinishedAction,
): FinishedState {
  switch (action.type) {
    case 'toggle':
      return { expanded: !state.expanded, visible: FINISHED_PAGE_SIZE }
    case 'more':
      return { ...state, visible: state.visible + FINISHED_PAGE_SIZE }
  }
}

export type SettleLabel =
  'merged' | 'done' | 'failed' | 'skipped' | 'paused' | 'stopped'

export interface SettleTally {
  label: SettleLabel
  count: number
}

export interface FinishedView {
  total: number
  tally: SettleTally[]
  latest?: TimelineTicket
  rows: TimelineTicket[]
  older: number
}

// buildTimeline hands settled tickets over oldest-first; finishedView reverses
// for newest-first display. Statuses nothing settled into drop out of the
// tally rather than showing a zero.
export function finishedView(
  settled: TimelineTicket[],
  visible: number,
): FinishedView {
  const rows = [...settled].reverse()
  const tally: SettleTally[] = [
    {
      label: 'merged',
      count: settled.filter((t) => t.status === 'done' && t.hasRun).length,
    },
    {
      label: 'done',
      count: settled.filter((t) => t.status === 'done' && !t.hasRun).length,
    },
    {
      label: 'failed',
      count: settled.filter((t) => t.status === 'failed').length,
    },
    {
      label: 'skipped',
      count: settled.filter((t) => t.status === 'skipped').length,
    },
    {
      label: 'paused',
      count: settled.filter((t) => t.status === 'paused').length,
    },
    {
      label: 'stopped',
      count: settled.filter((t) => t.status === 'stopped').length,
    },
  ]

  return {
    total: settled.length,
    tally: tally.filter((t) => t.count > 0),
    latest: rows[0],
    rows: rows.slice(0, visible),
    older: Math.max(0, settled.length - visible),
  }
}

export function ticketPill(t: TimelineTicket): {
  state: RunState
  label: string
} {
  switch (t.status) {
    case 'done':
      return { state: 'success', label: t.hasRun ? 'merged' : 'done' }
    case 'running':
      return stepPill(t.activity, t.phase ?? '')
    case 'paused':
      return { state: 'warn', label: 'paused' }
    case 'stopped':
      return { state: 'info', label: 'stopped' }
    case 'failed':
      return t.failureClass === 'gave_up'
        ? { state: 'fail', label: 'quarantined' }
        : { state: 'fail', label: 'fault' }
    case 'skipped':
      return { state: 'info', label: 'skipped' }
    case 'pending':
      return { state: 'todo', label: 'pending' }
  }
}
