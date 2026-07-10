import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface IssueDraft {
  title: string
  description?: string
  labels?: string[]
  parent?: string
}

// Issue is one ticket read straight from the repo's tracker, so the run-once
// form can confirm it before launching. Group is the normalized status bucket
// (backlog | unstarted | started | done | canceled | unknown) the form uses to
// warn about an unusual status.
export interface Issue {
  repo: string
  provider: string
  id: string
  title: string
  status: string
  group: string
  labels: string[]
  ready: boolean
  parent?: string
  // project is the ticket's own tracker project; in_project reports whether it
  // matches the repo's configured project, so a cross-project ticket can be
  // shown but refused rather than launched into the wrong repo.
  project?: string
  in_project: boolean
}

// IssueFetchKind separates the "expected" outcomes the form renders distinctly
// from a genuine transport error: a mistyped/absent ticket, and a repo with no
// direct tracker credentials.
export type IssueFetchKind = 'not-found' | 'no-tracker' | 'error'

export class IssueFetchError extends Error {
  kind: IssueFetchKind
  constructor(kind: IssueFetchKind, message: string) {
    super(message)
    this.name = 'IssueFetchError'
    this.kind = kind
  }
}

async function fetchIssue(repo: string, id: string): Promise<Issue> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/${encodeURIComponent(id)}`,
  )
  if (res.ok) return res.json()
  const message = await errorMessage(res, 'fetch ticket failed')
  if (res.status === 404) throw new IssueFetchError('not-found', message)
  if (res.status === 422) throw new IssueFetchError('no-tracker', message)
  throw new IssueFetchError('error', message)
}

export const issueQueryOptions = (repo: string, id: string) =>
  queryOptions({
    queryKey: ['issue', repo, id],
    queryFn: () => fetchIssue(repo, id),
    enabled: repo !== '' && id !== '',
    retry: false,
    staleTime: 15_000,
  })

export interface CreatedIssue {
  identifier: string
  url: string
  provider: string
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function createIssue(
  repo: string,
  draft: IssueDraft,
): Promise<CreatedIssue> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/issues`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(draft),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'create issue failed'))
  }
  return res.json()
}

export async function addComment(
  repo: string,
  ticket: string,
  body: string,
): Promise<void> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/comment`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'comment failed'))
  }
}
