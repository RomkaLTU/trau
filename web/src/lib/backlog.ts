import { keepPreviousData, queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import { type Assignee } from './assignee'

// BacklogEntry is one issue on the board, served from the hub's issue store.
// source distinguishes an internally-created issue (`internal`) from a synced
// tracker ticket (`linear` | `jira`); only internal issues are editable in place.
// assignee is null when the issue is unassigned. children_settled/children_total
// report an epic's settled (done + canceled) and total sub-issue counts over all
// children in the store, and are present only on an epic row (has_children) so the
// board can show its progress without a second call.
export interface BacklogEntry {
  id: string
  title: string
  status: string
  group: string
  labels: string[]
  source: string
  assignee?: Assignee | null
  parent?: string
  has_children: boolean
  children_settled?: number
  children_total?: number
  ready: boolean
}

// STATE_GROUPS is the board's normalized status vocabulary: every BacklogEntry
// lands in exactly one, and the state filter selects over them. `unknown` is the
// normalization fallback for a status that maps to no other group.
export const STATE_GROUPS = [
  'backlog',
  'unstarted',
  'started',
  'done',
  'canceled',
  'unknown',
] as const

export type StateGroup = (typeof STATE_GROUPS)[number]

export interface RepoFreshness {
  last_synced_at?: string
  syncing: boolean
  last_error?: string
  last_issues?: number
  last_comments?: number
}

export interface BacklogResponse {
  repo: string
  provider: string
  items: BacklogEntry[]
  // total is the number of matches before pagination, so the board can page.
  total: number
  // counts is the per-status-group match totals with the state filter ignored, so
  // section headers and the hidden-count hint hold whichever groups are on screen.
  counts: Record<string, number>
  freshness?: RepoFreshness
}

// BacklogParams are the board's filter and pagination controls, pushed to the
// server as query parameters. Empty fields are omitted, so the zero params is the
// unfiltered, unpaginated board.
export interface BacklogParams {
  state?: string
  label?: string
  source?: string
  q?: string
  limit?: number
  offset?: number
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

function backlogSearch(params: BacklogParams): string {
  const sp = new URLSearchParams()
  if (params.state) sp.set('state', params.state)
  if (params.label) sp.set('label', params.label)
  if (params.source) sp.set('source', params.source)
  if (params.q) sp.set('q', params.q)
  if (params.limit) sp.set('limit', String(params.limit))
  if (params.offset) sp.set('offset', String(params.offset))
  return sp.toString()
}

async function fetchBacklog(repo: string, params: BacklogParams): Promise<BacklogResponse> {
  const search = backlogSearch(params)
  const path = `/api/v1/repos/${encodeURIComponent(repo)}/backlog`
  const res = await apiFetch(search ? `${path}?${search}` : path)
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'backlog request failed'))
  }
  return res.json()
}

export const backlogQueryOptions = (repo: string, params: BacklogParams = {}) =>
  queryOptions({
    queryKey: ['backlog', repo, params],
    queryFn: () => fetchBacklog(repo, params),
    enabled: repo !== '',
    staleTime: 15_000,
    placeholderData: keepPreviousData,
  })

const SECTION_LABELS: Record<string, string> = {
  started: 'In Progress',
  unstarted: 'Todo',
  backlog: 'Backlog',
  unknown: 'Other',
  done: 'Done',
  canceled: 'Canceled',
}

export function sectionLabel(group: string): string {
  return SECTION_LABELS[group] ?? group
}

const TERMINAL_GROUPS = ['done', 'canceled'] as const

// GROUP_PRECEDENCE mirrors the hub's backlog ordering, so the board can locate
// where each group begins in the full result and tell a fresh section from one
// that merely continues across a page boundary.
const GROUP_PRECEDENCE = [
  'started',
  'unstarted',
  'backlog',
  'unknown',
  'done',
  'canceled',
] as const

export interface BacklogSection {
  group: string
  label: string
  count: number
  items: BacklogEntry[]
  // continuation is true when the group already began on an earlier page, so the
  // board renders its rows without repeating the header.
  continuation: boolean
}

function groupStartOffset(
  group: string,
  counts: Record<string, number>,
  activeGroups: string[],
): number {
  const active = new Set(activeGroups)
  let start = 0
  for (const g of GROUP_PRECEDENCE) {
    if (g === group) break
    if (active.has(g)) start += counts[g] ?? 0
  }
  return start
}

// backlogSections splits the hub-ordered rows into contiguous group segments so
// the board can render one header per group boundary. A group split across pages
// keeps a single header: the first segment of a page is flagged as a continuation
// when the page starts past that group's global offset.
export function backlogSections(
  items: BacklogEntry[],
  counts: Record<string, number>,
  activeGroups: string[] = [],
  offset = 0,
): BacklogSection[] {
  const sections: BacklogSection[] = []
  for (const entry of items) {
    const last = sections[sections.length - 1]
    if (last && last.group === entry.group) {
      last.items.push(entry)
      continue
    }
    sections.push({
      group: entry.group,
      label: sectionLabel(entry.group),
      count: counts[entry.group] ?? 0,
      items: [entry],
      continuation: false,
    })
  }
  const first = sections[0]
  if (first && offset > groupStartOffset(first.group, counts, activeGroups)) {
    first.continuation = true
  }
  return sections
}

export interface EpicRowNode {
  kind: 'epic'
  entry: BacklogEntry
  children: BacklogEntry[]
}

export interface FlatRowNode {
  kind: 'flat'
  entry: BacklogEntry
}

export type BacklogRowNode = EpicRowNode | FlatRowNode

// nestBacklogRows groups one section's hub-ordered rows into epic nodes and flat
// rows for status-true nesting. The hub orders each status group by family key
// with an epic immediately ahead of its same-group sub-issues, so an epic's
// children are the contiguous run of rows naming it as parent. A sub-issue whose
// epic row is absent — paged out, filtered away, or diverged into another
// section — stays a flat row and keeps its breadcrumb chip.
export function nestBacklogRows(items: BacklogEntry[]): BacklogRowNode[] {
  const nodes: BacklogRowNode[] = []
  for (const entry of items) {
    const last = nodes[nodes.length - 1]
    if (entry.parent && last?.kind === 'epic' && last.entry.id === entry.parent) {
      last.children.push(entry)
      continue
    }
    if (entry.has_children) {
      nodes.push({ kind: 'epic', entry, children: [] })
      continue
    }
    nodes.push({ kind: 'flat', entry })
  }
  return nodes
}

export interface HiddenGroupCount {
  group: string
  count: number
}

export function hiddenStateGroups(
  counts: Record<string, number>,
  activeGroups: string[],
): HiddenGroupCount[] {
  const active = new Set(activeGroups)
  const hidden: HiddenGroupCount[] = []
  for (const group of TERMINAL_GROUPS) {
    const count = counts[group] ?? 0
    if (count > 0 && !active.has(group)) {
      hidden.push({ group, count })
    }
  }
  return hidden
}
