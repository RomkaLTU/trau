import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

// PhaseLog is one phase's final agent output as the hub stored it (ADR 0008),
// keyed by the call label the phase ran under.
export interface PhaseLog {
  phase: string
  content: string
  updated: string
}

export interface PhaseLogsResponse {
  logs: PhaseLog[]
}

async function fetchPhaseLogs(repo: string, ticket: string): Promise<PhaseLogsResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(ticket)}/logs`,
  )
  if (!res.ok) throw new Error(`phase logs request failed: ${res.status}`)
  return res.json()
}

// The logs land as their phase finishes, which the run view already learns from
// the event feed — so this refetches on that invalidation rather than polling a
// response that carries every phase's full output.
export const phaseLogsQueryOptions = (repo: string, ticket: string) =>
  queryOptions({
    queryKey: ['phase-logs', repo, ticket],
    queryFn: () => fetchPhaseLogs(repo, ticket),
    enabled: repo !== '' && ticket !== '',
  })
