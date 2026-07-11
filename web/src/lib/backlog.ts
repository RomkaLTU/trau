import { queryOptions } from '@tanstack/react-query'

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
  freshness?: RepoFreshness
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

async function fetchBacklog(repo: string): Promise<BacklogResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/backlog`)
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'backlog request failed'))
  }
  return res.json()
}

export const backlogQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['backlog', repo],
    queryFn: () => fetchBacklog(repo),
    enabled: repo !== '',
    staleTime: 15_000,
  })
