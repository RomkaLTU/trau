import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import { type Assignee } from './assignee'

// AssigneeFacet is one entry in the board's assignee combobox: a distinct
// assignee carried by the repo's stored issues and how many of them it is
// assigned. me flags the repo's own identity so the Me row can pin first.
export interface AssigneeFacet extends Assignee {
  count: number
}

// AssigneesResponse pins the Me facet first (ADR 0014); unassigned is the count
// of issues with no assignee, filtered on with the `unassigned` token.
export interface AssigneesResponse {
  repo: string
  assignees: AssigneeFacet[]
  unassigned: number
}

async function fetchAssignees(repo: string): Promise<AssigneesResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/assignees`,
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `assignees request failed: ${res.status}`)
  }
  return res.json()
}

// assigneesQueryOptions feeds the board's assignee combobox. Like the labels
// facet it turns over only with the stored issue set, so it holds a longer
// staleTime than the backlog list and does not re-fetch on a filter keystroke.
export const assigneesQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['assignees', repo],
    queryFn: () => fetchAssignees(repo),
    enabled: repo !== '',
    staleTime: 5 * 60_000,
  })
