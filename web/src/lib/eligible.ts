import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface EligibleTicket {
  id: string
  title: string
  labels: string[]
}

export interface EligibleResponse {
  repo: string
  repo_root: string
  tickets: EligibleTicket[]
}

async function fetchEligible(repo: string): Promise<EligibleResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/eligible`,
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `eligible request failed: ${res.status}`)
  }
  return res.json()
}

export const eligibleQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['eligible', repo],
    queryFn: () => fetchEligible(repo),
    enabled: repo !== '',
    staleTime: 30_000,
  })
