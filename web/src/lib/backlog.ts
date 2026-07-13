import { keepPreviousData, queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

// BacklogEntry is one issue on the board, served from the hub's issue store.
// source distinguishes an internally-created issue (`internal`) from a synced
// tracker ticket (`linear` | `jira`); only internal issues are editable in place.
export interface BacklogEntry {
  id: string
  title: string
  status: string
  group: string
  labels: string[]
  source: string
  parent?: string
  has_children: boolean
  ready: boolean
}

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
