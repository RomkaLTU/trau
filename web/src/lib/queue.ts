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
  // provider is a per-item override of the configured routing — it applies only
  // to this item's run and never persists to config.
  provider?: string
  // provider_pin is the provider pinned on the underlying issue, which the run
  // uses whenever the item carries no override of its own.
  provider_pin?: string
  status: string
  reason?: string
  // blockers are the item's still-unresolved blocked-by edges; blocked is set
  // while it has any, so the row cannot be run on its own.
  blockers?: string[]
  blocked?: boolean
  // removing is set while a stop-then-remove is in flight, so a running row on
  // its way out of the queue reads as leaving rather than as work under way.
  removing?: boolean
  sub_issues?: QueueSubIssue[]
  queued_at?: string
}

export interface QueueResponse {
  repo: string
  draining: boolean
  // When the current drain was armed. Absent unless the queue is draining.
  draining_since?: string
  // stopping is set while a Stop is ending the child that was running, so the
  // row still reads running but the run is already on its way out.
  stopping: boolean
  shutting_down: boolean
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
  provider?: string
  // front lands the item in the first pending position instead of the back; a
  // pending item re-queued with front moves to the front instead of conflicting.
  front?: boolean
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

// enqueueFresh adds an item at the back, clearing the one conflict a re-add
// legitimately hits: a settled row keeps its slot as Finished history and still
// holds the id. A live row is a real duplicate and rethrows.
export async function enqueueFresh(
  repo: string,
  req: EnqueueRequest,
): Promise<QueueResponse> {
  try {
    return await enqueue(repo, req)
  } catch (err) {
    const { items } = await fetchQueue(repo)
    const leftover = items.find((it) => it.id === req.id)
    if (!leftover || !queueTerminal(leftover.status)) throw err
    await dequeue(repo, req.id)
    return enqueue(repo, req)
  }
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

// promoteQueueItem moves an item to the front of the run order, ahead of every
// row the drain would still launch, so it picks this one when the run in flight
// settles. A row that has started or settled meanwhile answers 409.
export async function promoteQueueItem(
  repo: string,
  id: string,
): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/${encodeURIComponent(id)}/move`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ to: 'front' }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'run next failed'))
  }
  return res.json()
}

// runQueueItem runs one queued item on its own: the hub spawns its child without
// arming the drain, so when the item settles the queue goes idle instead of
// starting the next row.
export async function runQueueItem(
  repo: string,
  id: string,
): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/${encodeURIComponent(id)}/run`,
    { method: 'POST' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'run item failed'))
  }
  return res.json()
}

// dequeue takes an item out of the queue without touching the ticket. A running
// item is refused unless stop is set, which asks the hub to stop the item's child
// first — the row then goes once the run has exited, and the answer describes the
// queue as it is mid-removal.
export async function dequeue(
  repo: string,
  id: string,
  opts: { stop?: boolean } = {},
): Promise<QueueResponse> {
  const query = opts.stop ? '?stop=1' : ''
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/${encodeURIComponent(id)}${query}`,
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

// stopQueue stops the loop where it stands: it disarms the drain and stops the
// child that was running, leaving the queue itself alone — the stopped item
// parks at its checkpoint and Start picks up from there. The hub ends the child
// asynchronously, so the answer can still carry a running row; `stopping` marks
// that wait until a later poll shows the item parked.
export async function stopQueue(repo: string): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/stop`,
    { method: 'POST' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'queue stop failed'))
  }
  return res.json()
}

// shutdownQueue tears the loop down: stops the running child, drops paused
// checkpoints, and clears the queue. The hub does this asynchronously — the
// response only acknowledges the request, so callers must poll the queue
// query to see `shutting_down` clear.
export async function shutdownQueue(repo: string): Promise<void> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/queue/shutdown`,
    { method: 'POST' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'shutdown failed'))
  }
}

// runNext is the one web launch gesture: make the item the queue's next run,
// then arm the drain. A fresh id front-inserts and a pending one moves to the
// front; on the conflict an already-queued id answers instead, a paused item is
// promoted so the arming resumes the ticket named here rather than whichever
// runnable row sits ahead of it, and a settled leftover is dropped and re-queued
// so the ticket runs again.
export async function runNext(
  repo: string,
  req: EnqueueRequest,
  opts: DrainOptions = {},
): Promise<QueueResponse> {
  try {
    await enqueue(repo, { ...req, front: true })
  } catch (err) {
    const { items } = await fetchQueue(repo)
    const queued = items.find((it) => it.id === req.id)
    if (!queued) throw err
    if (queueTerminal(queued.status)) {
      await dequeue(repo, req.id)
      await enqueue(repo, { ...req, front: true })
    } else if (queued.status === 'paused') {
      await promoteQueueItem(repo, req.id)
    }
  }
  return drain(repo, true, opts)
}

// runOnly runs one issue on its own without arming the drain: an id the queue is
// still going to act on runs where it stands, an unqueued one is appended first,
// and a settled leftover holding the id is dropped and re-queued so it runs again.
export async function runOnly(
  repo: string,
  req: EnqueueRequest,
): Promise<QueueResponse> {
  try {
    await enqueue(repo, req)
  } catch (err) {
    const { items } = await fetchQueue(repo)
    const queued = items.find((it) => it.id === req.id)
    if (!queued) throw err
    if (queueTerminal(queued.status)) {
      await dequeue(repo, req.id)
      await enqueue(repo, req)
    }
  }
  return runQueueItem(repo, req.id)
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

// queueLive reports whether the hub still has work in flight for this queue,
// which is what keeps the Loop view polling. A running row counts on its own: a
// Stop disarms the drain immediately but the child it is ending exits later, and
// the row only parks once the drain has seen it go.
export function queueLive(queue?: QueueResponse): boolean {
  if (!queue) return false
  return (
    queue.draining ||
    queue.stopping ||
    queue.shutting_down ||
    queue.items.some((it) => it.status === 'running')
  )
}

// queueTerminal reports whether an item has already settled: the drain only
// launches pending or paused items, so a done, failed, or skipped one no longer
// contributes work to a Start.
export function queueTerminal(status: string): boolean {
  return status === 'done' || status === 'failed' || status === 'skipped'
}

// queueStatusRunnable mirrors the hub's own queue.Runnable rule: the drain
// launches the first pending or paused item by position and passes over every
// settled one.
export function queueStatusRunnable(status: string): boolean {
  return status === 'pending' || status === 'paused'
}

// queueRunnable reports whether a Start has anything to launch. A paused epic
// counts even when every sub-issue reads done — the Start re-attempts the
// finalize the pause parked on, which runs no leaf ticket of its own.
export function queueRunnable(items: QueueItem[]): boolean {
  return items.some((it) => queueStatusRunnable(it.status))
}

// QUEUE_NOT_RUNNABLE is why a Start is unavailable, said both as a tooltip and
// as the reason next to the disabled control.
export const QUEUE_NOT_RUNNABLE =
  'Nothing to run — add a ticket or resume a paused item first'

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

// queueCoveredIds collects every id the queue holds a row for — each item's own
// id plus every sub-issue captured under a queued epic — settled rows included,
// since those keep their slot as Finished history.
export function queueCoveredIds(items: QueueItem[]): Set<string> {
  const covered = new Set<string>()
  for (const it of items) {
    covered.add(it.id)
    for (const sub of it.sub_issues ?? []) covered.add(sub.id)
  }
  return covered
}

// queueActiveIds narrows the coverage to the ids the queue is still going to act
// on, which is what "queued" means to an operator. A settled row — and the
// sub-issues under a settled epic — drops out, so the issue can be added again.
export function queueActiveIds(items: QueueItem[]): Set<string> {
  return queueCoveredIds(items.filter((it) => !queueTerminal(it.status)))
}

export interface QueueCounts {
  total: number
  tickets: number
  epics: number
}

// queueCounts summarizes a queue for the view header: the total registered and
// how it splits between single tickets and epics.
export function queueCounts(items: QueueItem[]): QueueCounts {
  const epics = items.filter((it) => it.kind === 'epic').length
  return { total: items.length, tickets: items.length - epics, epics }
}
