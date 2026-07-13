import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

// LabelFacet is one entry in the board's label combobox: a distinct label name
// carried by the repo's stored issues and how many of them carry it. Labels are
// grouped case-insensitively, matching the board's label filter.
export interface LabelFacet {
  name: string
  count: number
}

export interface LabelsResponse {
  repo: string
  labels: LabelFacet[]
}

async function fetchLabels(repo: string): Promise<LabelsResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/labels`)
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `labels request failed: ${res.status}`)
  }
  return res.json()
}

// labelsQueryOptions feeds the board's label combobox. The facet turns over only
// with the stored issue set, not with the active filters, so it holds a longer
// staleTime than the backlog list — the combobox does not re-fetch on a filter
// keystroke.
export const labelsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['labels', repo],
    queryFn: () => fetchLabels(repo),
    enabled: repo !== '',
    staleTime: 5 * 60_000,
  })
