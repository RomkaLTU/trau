import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Run } from './runs'

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

// The cross-repo groups each carry the repo their row came from, so a palette
// row under "All projects" can say where it lives and land in the right repo.
export interface GlobalIssueResult extends SearchResult {
  repo: string
}

// GlobalSettingResult carries a value the hub already masked — a secret never
// reaches the browser.
export interface GlobalSettingResult {
  repo: string
  key: string
  group: string
  value: string
  kind?: string
}

export interface GlobalRunResult extends Run {
  repo: string
}

export interface GlobalSearchResponse {
  query: string
  results: {
    issues: GlobalIssueResult[]
    settings: GlobalSettingResult[]
    runs: GlobalRunResult[]
  }
}

async function fetchGlobalSearch(query: string): Promise<GlobalSearchResponse> {
  const params = new URLSearchParams({ q: query })
  const res = await apiFetch(`/api/v1/search?${params.toString()}`)
  if (!res.ok) {
    throw new Error(`global search failed: ${res.status}`)
  }
  return res.json()
}

export const globalSearchQueryOptions = (query: string) =>
  queryOptions({
    queryKey: ['global-search', query],
    queryFn: () => fetchGlobalSearch(query.trim()),
    enabled: query.trim() !== '',
    staleTime: 30_000,
  })
