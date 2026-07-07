import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface BacklogEntry {
  id: string
  title: string
  status: string
  group: string
  labels: string[]
  parent?: string
  has_children: boolean
  ready: boolean
}

export interface BacklogResponse {
  repo: string
  provider: string
  items: BacklogEntry[]
}

// BacklogUnavailableError marks the config state where a repo has no direct
// tracker credentials, so the board renders an explicit backlog-unavailable
// notice instead of a generic error.
export class BacklogUnavailableError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'BacklogUnavailableError'
  }
}

async function fetchBacklog(repo: string): Promise<BacklogResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/backlog`)
  const detail = (await res.json().catch(() => null)) as
    | (Partial<BacklogResponse> & { error?: string })
    | null
  if (res.status === 422) {
    throw new BacklogUnavailableError(
      detail?.error ?? 'this repo has no direct tracker credentials',
    )
  }
  if (!res.ok) {
    throw new Error(detail?.error ?? `backlog request failed: ${res.status}`)
  }
  return detail as BacklogResponse
}

export const backlogQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['backlog', repo],
    queryFn: () => fetchBacklog(repo),
    enabled: repo !== '',
    staleTime: 30_000,
    retry: false,
  })

export type StatusGroupKey =
  | 'started'
  | 'unstarted'
  | 'backlog'
  | 'done'
  | 'canceled'
  | 'unknown'

const GROUP_LABELS: Record<StatusGroupKey, string> = {
  started: 'In progress',
  unstarted: 'Todo',
  backlog: 'Backlog',
  done: 'Done',
  canceled: 'Canceled',
  unknown: 'Other',
}

// STATUS_GROUP_ORDER draws active work at the top of the board: in-progress and
// todo first, then the backlog, with finished and canceled work last.
export const STATUS_GROUP_ORDER: StatusGroupKey[] = [
  'started',
  'unstarted',
  'backlog',
  'done',
  'canceled',
  'unknown',
]

export interface BacklogGroup {
  key: StatusGroupKey
  label: string
  items: BacklogEntry[]
}

function normalizeGroup(group: string): StatusGroupKey {
  return (group in GROUP_LABELS ? group : 'unknown') as StatusGroupKey
}

function issueNumber(id: string): number {
  const m = /-(\d+)$/.exec(id)
  return m ? Number.parseInt(m[1], 10) : 0
}

// compareBoard keeps epics at the top of each status column so a parent reads
// above its siblings, then orders by descending issue number (most recent first).
function compareBoard(a: BacklogEntry, b: BacklogEntry): number {
  if (a.has_children !== b.has_children) return a.has_children ? -1 : 1
  return issueNumber(b.id) - issueNumber(a.id)
}

// groupBacklog buckets the backlog into status-group columns in board order,
// dropping empty groups and ordering each group's rows with compareBoard.
export function groupBacklog(items: BacklogEntry[]): BacklogGroup[] {
  const groups: BacklogGroup[] = []
  for (const key of STATUS_GROUP_ORDER) {
    const bucket = items.filter((it) => normalizeGroup(it.group) === key)
    if (bucket.length === 0) continue
    bucket.sort(compareBoard)
    groups.push({ key, label: GROUP_LABELS[key], items: bucket })
  }
  return groups
}
