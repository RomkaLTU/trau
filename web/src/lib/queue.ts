import { queryOptions, type QueryClient } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Run } from './runs'

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
  // source is the issue's binding resolved when it was queued: 'internal' for a
  // hub-only issue, otherwise the tracker provider. Absent on items queued before
  // the hub recorded it.
  source?: string
  status: string
  reason?: string
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

// publishQueue writes the queue a mutation just returned into the cache, so the
// card redraws on the response rather than waiting for the next refetch.
export function publishQueue(
  client: QueryClient,
  repo: string,
  res: QueueResponse,
): void {
  client.setQueryData(queueQueryOptions(repo).queryKey, res)
}

export interface EnqueueRequest {
  id: string
  // kind is optional: omit it and the hub resolves ticket vs epic by looking
  // the id up in the tracker.
  kind?: QueueKind
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

export async function moveQueueItem(
  repo: string,
  id: string,
  dir: -1 | 1,
): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/${encodeURIComponent(id)}/move`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ dir }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'reorder failed'))
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

export type OnFault = 'halt' | 'skip'

// DrainOptions are the run-level knobs a Start carries: whether to ignore stored
// checkpoints, and what a fault does to the rest of the queue.
export interface DrainOptions {
  no_resume?: boolean
  on_fault?: OnFault
}

export async function drain(
  repo: string,
  draining: boolean,
  opts: DrainOptions = {},
): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/drain`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draining, ...opts }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'queue drain failed'))
  }
  return res.json()
}

// skipResumeApplies reports whether the Skip resume toggle would change anything
// for this queue, so the Loop card can hide a no-op control. It applies when the
// queue has already executed (any item past pending — Start restarts it from the
// top) or when a queued ticket or epic sub-issue has an in-flight, non-terminal
// run whose stored checkpoint a fresh Start would otherwise resume.
export function skipResumeApplies(items: QueueItem[], runs: Run[]): boolean {
  if (items.some((it) => it.status !== 'pending')) return true
  const inFlight = new Set(runs.filter((r) => !r.terminal).map((r) => r.ticket))
  if (inFlight.size === 0) return false
  return items.some(
    (it) =>
      inFlight.has(it.id) ||
      (it.sub_issues ?? []).some((s) => inFlight.has(s.id)),
  )
}

// queueTerminal reports whether an item has already settled: the drain only
// launches pending or paused items, so a done, failed, or skipped one no longer
// contributes work to a Start.
export function queueTerminal(status: string): boolean {
  return status === 'done' || status === 'failed' || status === 'skipped'
}

// queueExecutable estimates how many leaf tickets a Start will run: each
// unsettled ticket counts once, each epic by its not-done sub-issues (the count
// resolves lazily at run time, so this is the launch-time estimate).
export function queueExecutable(items: QueueItem[]): number {
  return items.reduce((n, it) => {
    if (it.kind !== 'epic') return n + (queueTerminal(it.status) ? 0 : 1)
    const subs = it.sub_issues ?? []
    return n + subs.filter((s) => s.state !== 'done').length
  }, 0)
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
