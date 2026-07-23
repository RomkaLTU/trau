import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Handback } from './handback'
import type { RepoView } from './instances'

export type FailureClass = 'paused' | 'stopped' | 'faulted' | 'gave_up'

export interface Run {
  ticket: string
  title?: string
  phase: string
  phase_rank: number
  terminal: boolean
  branch?: string
  pr?: string
  pr_url?: string
  failure_class?: FailureClass
  failure_reason?: string
  cost_usd?: number
  updated_at?: string
  handback?: Handback
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
