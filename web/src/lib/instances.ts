import { queryOptions } from '@tanstack/react-query'

import type { RunState } from '@/components/trau/status-pill'
import { apiFetch } from './api'

export interface Instance {
  pid: number
  repo: string
  repo_root: string
  runs_dir: string
  started_at: string
  session_state: string
  ticket?: string
  phase?: string
  activity?: string
  detail?: string
  state_since?: string
}

// RepoHealthState mirrors the hub's derived health. A recorded error is
// sync-failed even over a good synced stamp, so a repo whose seed sync failed
// never reads as ready.
export type RepoHealthState =
  | 'ready'
  | 'unconfigured'
  | 'sync-failed'
  | 'never-synced'
  | 'syncing'

// RepoFreshness is a repo's issue-store sync state: when it last synced from the
// tracker, whether a background sync is running right now, the last sync error,
// and the counts the last good sync wrote. Absent for a repo that has never
// synced and is not syncing. The repos API always carries a state and issue_count;
// the backlog attaches only the sync fields.
export interface RepoFreshness {
  state?: RepoHealthState
  last_synced_at?: string
  syncing: boolean
  last_error?: string
  last_issues?: number
  last_comments?: number
  issue_count?: number
}

export interface RepoView {
  name: string
  root: string
  runs_dir: string
  live: boolean
  allowed: boolean
  registered: boolean
  freshness?: RepoFreshness
}

// RepoHealth is one repo's /health resource: the derived state plus the sync
// facts behind it, so a gate can poll a single repo instead of the whole list.
export interface RepoHealth {
  repo: string
  state: RepoHealthState
  last_synced_at: string
  last_error: string
  issue_count: number
}

export interface InstancesResponse {
  instances: Instance[]
  repos: RepoView[]
}

// Only a store read error drops the freshness the repos API otherwise always
// sends; that repo reads as unconfigured rather than claiming health.
export function repoHealth(repo: RepoView): RepoHealthState {
  return repo.freshness?.state ?? 'unconfigured'
}

export function anySyncing(repos: readonly RepoView[]): boolean {
  return repos.some((repo) => repoHealth(repo) === 'syncing')
}

export function healthPill(state: RepoHealthState): {
  state: RunState
  label: string
} {
  switch (state) {
    case 'ready':
      return { state: 'success', label: 'ready' }
    case 'syncing':
      return { state: 'active', label: 'syncing' }
    case 'sync-failed':
      return { state: 'fail', label: 'sync failing' }
    case 'never-synced':
      return { state: 'warn', label: 'never synced' }
    case 'unconfigured':
      return { state: 'warn', label: 'not configured' }
  }
}

// healthBlocks reports whether a state stops a repo-scoped page from being
// trusted: nothing is configured to fetch, or the last sync recorded an error.
// A syncing or never-synced repo is mid-setup and left alone.
export function healthBlocks(state: RepoHealthState): boolean {
  return state === 'unconfigured' || state === 'sync-failed'
}

async function fetchInstances(): Promise<InstancesResponse> {
  const res = await apiFetch('/api/v1/instances')
  if (!res.ok) {
    throw new Error(`instances request failed: ${res.status}`)
  }
  return res.json()
}

export const instancesQueryOptions = queryOptions({
  queryKey: ['instances'],
  queryFn: fetchInstances,
  refetchInterval: 3000,
  // Keep polling in a backgrounded tab so the live tab title stays current while
  // a user watches a run from another tab.
  refetchIntervalInBackground: true,
})

async function fetchRepoHealth(repo: string): Promise<RepoHealth> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/health`)
  if (!res.ok) {
    throw new Error(`repo health request failed: ${res.status}`)
  }
  return res.json()
}

// Keyed by repo so every gate on a page shares one fetch rather than one per
// section.
export const repoHealthQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['repo-health', repo],
    queryFn: () => fetchRepoHealth(repo),
    enabled: repo !== '',
    staleTime: 15_000,
  })

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export interface StopResult {
  status: string
  signal: string
}

export async function stopInstance(pid: number): Promise<StopResult> {
  const res = await apiFetch(`/api/v1/instances/${pid}/stop`, { method: 'POST' })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'stop failed'))
  }
  return res.json()
}

export async function registerRepo(path: string): Promise<RepoView> {
  const res = await apiFetch('/api/v1/repos', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'register failed'))
  }
  return res.json()
}

export async function unregisterRepo(repo: string): Promise<RepoView> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'unregister failed'))
  }
  return res.json()
}

export interface SyncResponse {
  repo: string
  provider: string
  issues: number
  comments: number
  syncedAt: string
}

// syncRepo pulls the repo's tracker project into the hub issue store, blocking
// for the length of the pull.
export async function syncRepo(repo: string): Promise<SyncResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/sync`, {
    method: 'POST',
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'sync failed'))
  }
  return res.json()
}

export interface DryRunResult {
  repo: string
  repo_root: string
  ticket: string
}

export async function dryRun(repo: string): Promise<DryRunResult> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/dry-run`,
    { method: 'POST' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'dry-run failed'))
  }
  return res.json()
}
