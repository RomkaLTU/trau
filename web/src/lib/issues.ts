import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

// IssueComment is one comment on an issue as the store returns it: author, body
// (markdown), and the tracker's created timestamp.
export interface IssueComment {
  author: string
  body: string
  created_at?: string
}

// Issue is one ticket read store-first from the hub's issue store (ADR 0007): the
// run-once form confirms it before launching, and the backlog drawer reads its
// full content in place. Group is the normalized status bucket (backlog |
// unstarted | started | done | canceled | unknown) the form uses to warn about an
// unusual status. Description and comments are the stored content; source and url
// come from the store (a synced ticket carries the tracker url, an internal one
// does not).
export interface Issue {
  repo: string
  provider: string
  id: string
  title: string
  description: string
  status: string
  group: string
  labels: string[]
  ready: boolean
  parent?: string
  source?: string
  has_children: boolean
  comments: IssueComment[]
  url?: string
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

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

// InternalIssueDraft is the editable content of an issue that lives only in the
// hub store (ADR 0007): title, markdown description, workflow state (a status
// group), labels, and an optional parent identifier nesting it under an epic.
export interface InternalIssueDraft {
  title: string
  description?: string
  state?: string
  labels?: string[]
  parent?: string
}

// InternalIssue is a stored internal issue as the create/edit forms read it — its
// allocated PREFIX-N identifier, content, normalized state and display status.
export interface InternalIssue {
  repo: string
  id: string
  title: string
  description: string
  state: string
  status: string
  labels: string[]
  parent?: string
  source: string
  has_children: boolean
}

// InternalState is the set of workflow states an internal issue can hold; these
// mirror the store's normalized status groups.
export const INTERNAL_STATES = [
  'backlog',
  'unstarted',
  'started',
  'done',
  'canceled',
] as const

export async function createInternalIssue(
  repo: string,
  draft: InternalIssueDraft,
): Promise<InternalIssue> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/internal`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(draft),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'create issue failed'))
  }
  return res.json()
}

async function fetchInternalIssue(repo: string, id: string): Promise<InternalIssue> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/internal/${encodeURIComponent(id)}`,
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'fetch issue failed'))
  }
  return res.json()
}

export const internalIssueQueryOptions = (repo: string, id: string) =>
  queryOptions({
    queryKey: ['internal-issue', repo, id],
    queryFn: () => fetchInternalIssue(repo, id),
    enabled: repo !== '' && id !== '',
    retry: false,
  })

export async function updateInternalIssue(
  repo: string,
  id: string,
  draft: InternalIssueDraft,
): Promise<InternalIssue> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/internal/${encodeURIComponent(id)}`,
    {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(draft),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'update issue failed'))
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
