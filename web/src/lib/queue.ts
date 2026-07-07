import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export type QueueKind = 'ticket' | 'epic'

export interface QueueSubIssue {
  id: string
  title: string
  state: string
}

export interface QueueItem {
  position: number
  kind: QueueKind
  id: string
  title?: string
  status: string
  sub_issues?: QueueSubIssue[]
  queued_at?: string
}

export interface QueueResponse {
  repo: string
  draining: boolean
  items: QueueItem[]
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

async function fetchQueue(repo: string): Promise<QueueResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/queue`)
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'queue request failed'))
  }
  return res.json()
}

export const queueQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['queue', repo],
    queryFn: () => fetchQueue(repo),
    enabled: repo !== '',
    staleTime: 15_000,
  })

export interface EnqueueRequest {
  kind: QueueKind
  id: string
  title?: string
}

export async function enqueue(
  repo: string,
  req: EnqueueRequest,
): Promise<QueueResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/queue`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'add to queue failed'))
  }
  return res.json()
}

export async function dequeue(
  repo: string,
  id: string,
): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/${encodeURIComponent(id)}`,
    { method: 'DELETE' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'remove from queue failed'))
  }
  return res.json()
}

export async function drain(
  repo: string,
  draining: boolean,
): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/drain`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draining }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'queue drain failed'))
  }
  return res.json()
}

export interface QueueCounts {
  total: number
  tickets: number
  epics: number
}

// queueCounts summarizes a queue for the view header: the total registered and
// how it splits between run-once tickets and epics.
export function queueCounts(items: QueueItem[]): QueueCounts {
  const epics = items.filter((it) => it.kind === 'epic').length
  return { total: items.length, tickets: items.length - epics, epics }
}
