import { useQueries, useQuery } from '@tanstack/react-query'

import { backlogQueryOptions, type BacklogEntry } from './backlog'
import { DEFAULT_STATE_GROUPS } from './backlog-filters'
import {
  activeSessionForIssue,
  GRILLABLE_LABELS,
  grillSessionsQueryOptions,
  isAwaitingAnswer,
  type GrillSession,
} from './grill'

// InboxAttention is why an unclear issue is in the inbox, driven by its active
// grilling session: answer = a question is waiting on the user (waiting/parked/
// stalled), thinking = the agent is mid-turn, open = untouched (no session yet),
// review = a finished proposal awaiting apply. It doubles as the list's sort tier.
export type InboxAttention = 'answer' | 'thinking' | 'open' | 'review'

const ATTENTION_ORDER: Record<InboxAttention, number> = {
  answer: 0,
  thinking: 1,
  open: 2,
  review: 3,
}

// InboxItem pairs a grillable issue with its active session (if any) and the
// resulting attention tier.
export interface InboxItem {
  entry: BacklogEntry
  session?: GrillSession
  attention: InboxAttention
}

export interface InboxSection {
  attention: InboxAttention
  items: InboxItem[]
}

export interface InboxCounts {
  total: number
  // awaiting is the parked-awaiting-answer count — the "a question is waiting for
  // you" figure the nav badge emphasises.
  awaiting: number
}

// inboxAttention classifies an issue by its active (unsettled) session. No session
// is an untouched issue; the awaiting-answer states surface first.
export function inboxAttention(session?: GrillSession): InboxAttention {
  if (!session) return 'open'
  if (isAwaitingAnswer(session.state)) return 'answer'
  if (session.state === 'finished') return 'review'
  return 'thinking'
}

// mergeGrillableEntries flattens the per-label backlog pages into one board, keyed
// by id so an issue carrying two triage labels appears once, ordered by workflow
// group then numeric-aware id to match the hub's board ordering.
export function mergeGrillableEntries(
  lists: readonly (readonly BacklogEntry[] | undefined)[],
): BacklogEntry[] {
  const byId = new Map<string, BacklogEntry>()
  for (const list of lists) {
    for (const entry of list ?? []) {
      if (!byId.has(entry.id)) byId.set(entry.id, entry)
    }
  }
  return [...byId.values()].sort(compareEntries)
}

// buildInbox attaches each issue's active session and sorts the board into
// attention tiers — answer, thinking, open, review — keeping the canonical order
// within a tier (a stable sort over already-ordered entries).
export function buildInbox(
  entries: readonly BacklogEntry[],
  sessions: GrillSession[] = [],
): InboxItem[] {
  const items = entries.map((entry) => {
    const session = activeSessionForIssue(sessions, entry.id)
    return { entry, session, attention: inboxAttention(session) }
  })
  return items.sort(
    (a, b) => ATTENTION_ORDER[a.attention] - ATTENTION_ORDER[b.attention],
  )
}

// inboxSections splits the tier-sorted items into contiguous runs so the list can
// render one header per attention group.
export function inboxSections(items: readonly InboxItem[]): InboxSection[] {
  const sections: InboxSection[] = []
  for (const item of items) {
    const last = sections[sections.length - 1]
    if (last && last.attention === item.attention) last.items.push(item)
    else sections.push({ attention: item.attention, items: [item] })
  }
  return sections
}

export function inboxCounts(items: readonly InboxItem[]): InboxCounts {
  let awaiting = 0
  for (const item of items) {
    if (item.attention === 'answer') awaiting++
  }
  return { total: items.length, awaiting }
}

// inboxPosition is the zero-based index of an issue in the walk-through, or -1 when
// it has left the list (e.g. its outcome was just applied).
export function inboxPosition(items: readonly InboxItem[], id: string): number {
  return items.findIndex((item) => item.entry.id === id)
}

// nextIssueId / prevIssueId step the walk-through. next past the last item and prev
// before the first both return null, which the drawer reads as "close".
export function nextIssueId(items: readonly InboxItem[], id: string): string | null {
  const at = inboxPosition(items, id)
  if (at === -1) return null
  return items[at + 1]?.entry.id ?? null
}

export function prevIssueId(items: readonly InboxItem[], id: string): string | null {
  const at = inboxPosition(items, id)
  if (at <= 0) return null
  return items[at - 1].entry.id
}

const GROUP_ORDER: Record<string, number> = {
  started: 0,
  unstarted: 1,
  backlog: 2,
  unknown: 3,
  done: 4,
  canceled: 5,
}

function compareEntries(a: BacklogEntry, b: BacklogEntry): number {
  const g = (GROUP_ORDER[a.group] ?? 9) - (GROUP_ORDER[b.group] ?? 9)
  return g !== 0 ? g : compareIssueIds(a.id, b.id)
}

// compareIssueIds orders identifiers numerically within a prefix so COD-9 precedes
// COD-100; a non-numeric or mismatched suffix falls back to a plain string compare.
export function compareIssueIds(a: string, b: string): number {
  const [pa, na] = splitId(a)
  const [pb, nb] = splitId(b)
  if (pa !== pb) return pa < pb ? -1 : 1
  if (Number.isNaN(na) || Number.isNaN(nb)) return a < b ? -1 : a > b ? 1 : 0
  return na - nb
}

function splitId(id: string): [string, number] {
  const dash = id.lastIndexOf('-')
  if (dash === -1) return [id, Number.NaN]
  const num = Number(id.slice(dash + 1))
  return Number.isNaN(num) ? [id, Number.NaN] : [id.slice(0, dash + 1), num]
}

// grillableBacklogQueries reuses the backlog endpoint's label filter — one open-
// state query per triage label — instead of duplicating a "grillable" server
// filter. The union is merged client-side by mergeGrillableEntries.
function grillableBacklogQueries(repo: string) {
  const state = [...DEFAULT_STATE_GROUPS].join(',')
  return GRILLABLE_LABELS.map((label) => backlogQueryOptions(repo, { label, state }))
}

export interface InboxData {
  items: InboxItem[]
  isLoading: boolean
  error: Error | null
}

// useInbox assembles the triage inbox for a repo: the union of open, triage-labelled
// issues joined to their active grilling sessions. Shared by the page and the nav
// badge; react-query dedupes the underlying fetches.
export function useInbox(repo: string): InboxData {
  const backlogs = useQueries({ queries: grillableBacklogQueries(repo) })
  const sessions = useQuery(grillSessionsQueryOptions(repo))
  const entries = mergeGrillableEntries(backlogs.map((q) => q.data?.items))
  const items = buildInbox(entries, sessions.data?.sessions ?? [])
  const failed = backlogs.find((q) => q.error)?.error ?? sessions.error
  return {
    items,
    isLoading: backlogs.some((q) => q.isLoading) || sessions.isLoading,
    error: (failed as Error) ?? null,
  }
}

export function useInboxCounts(repo: string): InboxCounts {
  return inboxCounts(useInbox(repo).items)
}
