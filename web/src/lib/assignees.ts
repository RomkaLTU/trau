import { queryOptions, type QueryClient } from '@tanstack/react-query'

import { apiFetch } from './api'
import { type Assignee } from './assignee'
import { issueQueryOptions, type Issue } from './issues'

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

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

async function fetchAssignees(repo: string): Promise<AssigneesResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/assignees`,
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'assignees request failed'))
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

// AssignableUsersResponse is who a repo's issues can be assigned to, read live from
// the tracker. The tracker caps the page, so the picker narrows with a query rather
// than paging a whole workspace. Me is pinned first (ADR 0014).
export interface AssignableUsersResponse {
  users: Assignee[]
}

// unsupported is what a provider with no assignment API answers, and sends the
// picker back to the read-only assignee row.
export type AssignErrorKind = 'unsupported' | 'error'

export class AssignError extends Error {
  kind: AssignErrorKind
  constructor(kind: AssignErrorKind, message: string) {
    super(message)
    this.name = 'AssignError'
    this.kind = kind
  }
}

export function isAssignUnsupported(err: unknown): boolean {
  return err instanceof AssignError && err.kind === 'unsupported'
}

async function assignError(
  res: Response,
  fallback: string,
): Promise<AssignError> {
  return new AssignError(
    res.status === 409 ? 'unsupported' : 'error',
    await errorMessage(res, fallback),
  )
}

async function fetchAssignableUsers(
  repo: string,
  query: string,
): Promise<AssignableUsersResponse> {
  const params = new URLSearchParams({ query })
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/assignable-users?${params.toString()}`,
  )
  if (!res.ok) {
    throw await assignError(res, 'assignable users request failed')
  }
  return res.json()
}

// Reaches the tracker itself rather than the store, so it is held only long enough
// to ride out a keystroke. The unfiltered read doubles as the picker's support
// probe, which is why an unsupported tracker is typed rather than merely failing.
export const assignableUsersQueryOptions = (repo: string, query: string) =>
  queryOptions({
    queryKey: ['assignable-users', repo, query],
    queryFn: () => fetchAssignableUsers(repo, query),
    enabled: repo !== '',
    retry: false,
    staleTime: 60_000,
  })

// assignIssue writes through to the tracker and answers with the mirrored issue; a
// null assignee unassigns it. A refused write leaves the stored assignee untouched.
export async function assignIssue(
  repo: string,
  id: string,
  assignee: Assignee | null,
): Promise<Issue> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/${encodeURIComponent(id)}/assignee`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: assignee?.id ?? '',
        name: assignee?.name ?? '',
      }),
    },
  )
  if (!res.ok) {
    throw await assignError(res, 'assign failed')
  }
  return res.json()
}

export function publishAssignment(
  client: QueryClient,
  repo: string,
  issue: Issue,
): void {
  client.setQueryData(issueQueryOptions(repo, issue.id).queryKey, issue)
  void client.invalidateQueries({ queryKey: ['backlog', repo] })
  void client.invalidateQueries({ queryKey: ['assignees', repo] })
}
