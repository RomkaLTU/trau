import type { FeedEvent, RepoFeedEvent } from '@/lib/events'

export const STATE_CHANGE_KIND = 'state_change'

export type RecapCategory = 'merged' | 'paused' | 'faulted' | 'quarantined'

export const RECAP_CATEGORIES: RecapCategory[] = [
  'merged',
  'paused',
  'faulted',
  'quarantined',
]

export type PauseReason = 'usage_window' | 'reauth' | 'other'

export interface RecapItem {
  key: string
  repo: string
  ticket: string
  state: RecapCategory
  reason: string
  ts: string
}

export interface Recap {
  since: string | null
  merged: RecapItem[]
  paused: RecapItem[]
  faulted: RecapItem[]
  quarantined: RecapItem[]
  total: number
}

export function eventKey(ev: RepoFeedEvent): string {
  return `${ev.repo}:${ev.id}`
}

// deriveRecap folds the machine-wide event history into the "since you were away"
// summary: every state_change that landed after the last-seen marker, bucketed by
// its terminal state. It is a pure fold over the events — the contract the recap
// UI and its fixtures both hold to.
export function deriveRecap(
  events: RepoFeedEvent[],
  since: string | null,
): Recap {
  const sinceMs = since ? Date.parse(since) : null
  const recap: Recap = {
    since,
    merged: [],
    paused: [],
    faulted: [],
    quarantined: [],
    total: 0,
  }
  const seen = new Set<string>()

  for (const ev of events) {
    const item = toRecapItem(ev)
    if (!item) continue
    if (sinceMs !== null) {
      const ms = Date.parse(ev.ts)
      if (!Number.isNaN(ms) && ms <= sinceMs) continue
    }
    if (seen.has(item.key)) continue
    seen.add(item.key)
    recap[item.state].push(item)
  }

  for (const cat of RECAP_CATEGORIES) {
    recap[cat].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts))
    recap.total += recap[cat].length
  }
  return recap
}

// toRecapItem projects a single event onto a RecapItem, or null when it is not a
// state_change the recap tracks. Shared by the fold and the live notification path
// so both read the same fields the same way.
export function toRecapItem(ev: RepoFeedEvent): RecapItem | null {
  if (ev.kind !== STATE_CHANGE_KIND) return null
  const state = fieldStr(ev, 'state')
  if (!isRecapCategory(state)) return null
  return {
    key: eventKey(ev),
    repo: ev.repo,
    ticket: fieldStr(ev, 'ticket'),
    state,
    reason: fieldStr(ev, 'reason'),
    ts: ev.ts,
  }
}

export function pauseReason(item: RecapItem): PauseReason {
  if (item.reason === 'usage_window') return 'usage_window'
  if (item.reason === 'reauth') return 'reauth'
  return 'other'
}

// describeRecapItem is the single phrasing shared by the recap list and the browser
// notification, so both name a transition the same way.
export function describeRecapItem(item: RecapItem): string {
  const who = item.ticket || item.repo
  switch (item.state) {
    case 'merged':
      return `${who} merged`
    case 'faulted':
      return item.reason ? `${who} faulted during ${item.reason}` : `${who} faulted`
    case 'quarantined':
      return item.reason
        ? `${who} quarantined — ${item.reason}`
        : `${who} quarantined`
    case 'paused':
      switch (pauseReason(item)) {
        case 'usage_window':
          return `${who} paused — usage window`
        case 'reauth':
          return `${who} paused — needs re-auth`
        default:
          return `${who} paused`
      }
  }
}

function fieldStr(ev: FeedEvent, key: string): string {
  const v = ev.fields?.[key]
  return typeof v === 'string' ? v : ''
}

function isRecapCategory(s: string): s is RecapCategory {
  return (RECAP_CATEGORIES as string[]).includes(s)
}
