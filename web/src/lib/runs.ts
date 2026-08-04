import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Handback } from './handback'
import type { RepoView } from './instances'

export type FailureClass =
  | 'paused'
  | 'stopped'
  | 'budget'
  | 'faulted'
  | 'gave_up'

export type PRStatus = 'awaiting-merge' | 'merged' | 'closed'

// ReleaseMarker rides beside an Epic's releasing phase and says who owns the
// release: trau while it is still working it, a person once it handed the PR over.
export type ReleaseMarker = 'active' | 'awaiting-human'

export interface ActivitySpan {
  activity: string
  duration_ms: number
}

// SharedRun is everything a teammate's run carried across the team-sync remote,
// and the author to attribute it to.
export interface SharedRun {
  writer: string
  author?: string
  repo?: string
  started_at?: string
  ended_at?: string
  provider?: string
  model?: string
  tokens?: number
  activities?: ActivitySpan[]
  fetched_at?: string
}

// RunShip is one Child repo a Folder repo run shipped to and the pull request the
// branch it cut there carries. A run has one entry per changed child; pr is absent
// until that child's PR is opened.
export interface RunShip {
  repo: string
  pr?: string
  url?: string
}

export interface Run {
  ticket: string
  title?: string
  phase: string
  phase_rank: number
  terminal: boolean
  branch?: string
  pr?: string
  pr_url?: string
  pr_status?: PRStatus
  release?: ReleaseMarker
  failure_class?: FailureClass
  failure_reason?: string
  cost_usd?: number
  updated_at?: string
  handback?: Handback
  shared?: SharedRun
  ships?: RunShip[]
}

export interface ReposResponse {
  repos: RepoView[]
}

export interface RunsResponse {
  repo: string
  runs: Run[]
}

async function fetchRepos(): Promise<ReposResponse> {
  const res = await apiFetch('/api/v1/repos')
  if (!res.ok) {
    throw new Error(`repos request failed: ${res.status}`)
  }
  return res.json()
}

async function fetchRuns(repo: string): Promise<RunsResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/runs`)
  if (!res.ok) {
    throw new Error(`runs request failed: ${res.status}`)
  }
  return res.json()
}

export const reposQueryOptions = queryOptions({
  queryKey: ['repos'],
  queryFn: fetchRepos,
  refetchInterval: 5000,
})

export const runsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['runs', repo],
    queryFn: () => fetchRuns(repo),
    refetchInterval: 3000,
    enabled: repo !== '',
  })

async function fetchTeamRuns(repo: string): Promise<RunsResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/team-runs`,
  )
  if (!res.ok) {
    throw new Error(`team runs request failed: ${res.status}`)
  }
  return res.json()
}

// teamRunsQueryOptions reads the runs teammates shared over the repo's git remote.
// They are a resource of their own so nothing that means "this machine's work" ever
// picks them up; only the ledger and the ticket drawer merge them in, for display.
export const teamRunsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['team-runs', repo],
    queryFn: () => fetchTeamRuns(repo),
    refetchInterval: 15000,
    enabled: repo !== '',
  })
