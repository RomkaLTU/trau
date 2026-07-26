import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface TeamSyncStatus {
  repo: string
  enabled: boolean
  has_remote: boolean
  remote: string
  writer_id?: string
  author?: string
  teammates: number
  last_sync_at?: string
  last_error?: string
}

function teamSyncPath(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/team-sync`
}

async function fetchTeamSync(repo: string): Promise<TeamSyncStatus> {
  const res = await apiFetch(teamSyncPath(repo))
  if (!res.ok) {
    throw new Error(`team sync request failed: ${res.status}`)
  }
  return res.json()
}

export async function syncTeamNow(repo: string): Promise<TeamSyncStatus> {
  const res = await apiFetch(teamSyncPath(repo), { method: 'POST' })
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `sync failed: ${res.status}`)
  }
  return res.json()
}

export const teamSyncQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['team-sync', repo],
    queryFn: () => fetchTeamSync(repo),
    enabled: repo !== '',
  })

const TEAM_SYNC_TERMS = [
  'team sync',
  'sync now',
  'shared lessons',
  'git remote',
  'teammates',
  'publishing as',
  'writer id',
  'last sync',
  'last error',
]

export function matchesTeamSync(query: string): boolean {
  if (query === '') return true
  const q = query.toLowerCase()
  return TEAM_SYNC_TERMS.some((term) => term.includes(q))
}

export function lastSyncLabel(status: TeamSyncStatus): string {
  if (!status.last_sync_at) return 'never'
  const d = new Date(status.last_sync_at)
  return Number.isNaN(d.getTime())
    ? status.last_sync_at
    : d.toLocaleString()
}
