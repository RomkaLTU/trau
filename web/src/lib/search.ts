import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

// SearchResult is one ranked issue match from the hub's local store, enough to
// render a result row and link to the ticket.
export interface SearchResult {
  id: string
  title: string
  status: string
  group: string
  source: string
  labels: string[]
  parent?: string
  has_children: boolean
  url?: string
}

export interface SearchResponse {
  repo: string
  query: string
  results: SearchResult[]
}

async function fetchSearch(repo: string, query: string): Promise<SearchResponse> {
  const params = new URLSearchParams({ q: query })
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/search?${params.toString()}`,
  )
  if (!res.ok) {
    throw new Error(`issue search failed: ${res.status}`)
  }
  return res.json()
}

export const issueSearchQueryOptions = (repo: string, query: string) =>
  queryOptions({
    queryKey: ['issue-search', repo, query],
    queryFn: () => fetchSearch(repo, query.trim()),
    enabled: repo !== '' && query.trim() !== '',
    staleTime: 30_000,
  })
