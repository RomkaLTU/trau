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
  // skips are the canonical keys naming the pipeline work this item's run
  // bypasses, absent on an item that skips nothing.
  skips?: string[]
  status: string
  reason?: string
  // blockers are the item's still-unresolved blocked-by edges; blocked is set
  // while it has any, so the row cannot be run on its own.
  blockers?: string[]
  blocked?: boolean
  // batch names the batch holding the item, absent when none does.
  batch?: string
  sub_issues?: QueueSubIssue[]
  queued_at?: string
}

// QueueBatch is a named subset of the queue. The name may be empty, and the
// surfaces then label the batch by when it was filed.
export interface QueueBatch {
  id: string
  name: string
  created_at: string
}

export interface QueueResponse {
  repo: string
  draining: boolean
  // When the current drain was armed. Absent unless the queue is draining.
  draining_since?: string
  // draining_batch names the batch the drain in flight is scoped to, empty while
  // it is draining the whole queue.
  draining_batch?: string
  // stopping is set while a Stop is ending the child that was running, so the
  // row still reads running but the run is already on its way out.
  stopping: boolean
  // releasing_epic names the epic whose release holds the queue: while it is set
  // the hub starts no new run in the repo. Absent when nothing gates it, and on a
  // repo with worktrees, where a releasing epic holds only its own lane.
  releasing_epic?: string
  // lanes is how many runs the repo may have in flight at once — WORKTREE_PARALLEL
  // where worktrees isolate the trees, 1 everywhere else.
  lanes?: number
  // held reports a drain that is armed and starting nothing anyway, with held_gate
  // naming the gate in the hub's hold vocabulary, held_reason spelling it out and
  // held_since saying when the wait began.
  held?: boolean
  held_gate?: string
  held_reason?: string
  held_since?: string
  batches?: QueueBatch[]
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
  // skips are the canonical keys naming the pipeline work this run bypasses.
  // The hub refuses an unknown one, and a pending item re-queued with front
  // adopts the set, so the latest gesture describes the run.
  skips?: string[]
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

// enqueueOnce adds an item at the back and reads an id the queue is already
// going to act on as done rather than as a conflict, so a gesture repeated on an
// issue it already queued settles instead of failing.
export async function enqueueOnce(
  repo: string,
  req: EnqueueRequest,
): Promise<QueueResponse> {
  try {
    return await enqueueFresh(repo, req)
  } catch (err) {
    const queue = await fetchQueue(repo)
    if (!queueActiveIds(queue.items).has(req.id)) throw err
    return queue
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

// requeueIssue makes a quarantined ticket eligible again: the hub drives the
// same trau --requeue the CLI offers — tracker labels and status restored, the
// attempt PR closed, its branches dropped, the checkpoint cleared — then repairs
// the queue snapshot and answers with it, so the quarantine stops being reported
// anywhere the queue is read.
export async function requeueIssue(
  repo: string,
  id: string,
): Promise<QueueResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/${encodeURIComponent(id)}/requeue`,
    { method: 'POST' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'requeue failed'))
  }
  return res.json()
}

// dequeue takes an item out of the queue without touching the ticket. A running
// item is refused: the run is stopped first — which parks it resumably — and the
// parked row is what a removal then takes out.
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
// checkpoints, what a fault does to the rest of the queue, and the pipeline work
// the launch bypasses. The hub adds skips on top of each targeted item's stored
// set, so a launch-time choice never drops one made when an item was queued.
export interface DrainOptions {
  no_resume?: boolean
  on_fault?: OnFault
  skips?: string[]
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

function batchesURL(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/queue/batches`
}

// createBatch files queued items under one batch, named or not. The hub refuses
// an id that has already settled or that another batch holds.
export async function createBatch(
  repo: string,
  ids: string[],
  name?: string,
): Promise<QueueResponse> {
  const res = await apiFetch(batchesURL(repo), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, ids }),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'create batch failed'))
  }
  return res.json()
}

// BatchEdit is what a batch can be changed to: a rename — sent empty it clears
// the name back to the stamp the display falls back to — and the membership to
// take in or let go.
export interface BatchEdit {
  name?: string
  add?: string[]
  remove?: string[]
}

export async function updateBatch(
  repo: string,
  id: string,
  edit: BatchEdit,
): Promise<QueueResponse> {
  const res = await apiFetch(`${batchesURL(repo)}/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(edit),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'edit batch failed'))
  }
  return res.json()
}

// dismissBatch dissolves the grouping alone: every member keeps its place in the
// queue and its status.
export async function dismissBatch(
  repo: string,
  id: string,
): Promise<QueueResponse> {
  const res = await apiFetch(`${batchesURL(repo)}/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'dismiss batch failed'))
  }
  return res.json()
}

// startBatch arms the drain scoped to one batch: it runs the batch's members in
// queue order and disarms once none of them is runnable, however much of the
// queue is still pending behind them.
export async function startBatch(
  repo: string,
  id: string,
  opts: DrainOptions = {},
): Promise<QueueResponse> {
  const res = await apiFetch(
    `${batchesURL(repo)}/${encodeURIComponent(id)}/start`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(opts),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'start batch failed'))
  }
  return res.json()
}

// runNext is the one web launch gesture: make the item the queue's next run,
// then arm the drain. A fresh id front-inserts and a runnable one — pending or
// paused — moves to the front adopting this gesture's skip set, so the arming
// resumes the ticket named here rather than whichever runnable row sits ahead of
// it. On the conflict a settled leftover is dropped and re-queued so the ticket
// runs again, and an id the queue is already acting on answers as it stands.
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
    }
  }
  return drain(repo, true, opts)
}

// runOnly runs one issue on its own without arming the drain: an unqueued one is
// appended first, and a settled leftover holding the id is dropped and re-queued
// so it runs again. A runnable row already holding the id — pending or paused —
// is re-landed at the front, which is where the row this call is about to run
// belongs anyway, so it adopts the skip set this gesture chose instead of running
// the stale one.
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
    } else if (queueStatusRunnable(queued.status)) {
      await enqueue(repo, { ...req, front: true })
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
    queue.items.some((it) => it.status === 'running')
  )
}

// releaseGateLabel says what a gated queue is waiting for: while an epic's
// release is still trau's to finish the hub starts nothing else in the repo, so
// the drain names that epic instead of reading idle. Empty when nothing gates the
// queue, a release handed to a human included.
export function releaseGateLabel(queue?: QueueResponse): string {
  const epic = queue?.releasing_epic
  return epic ? `waiting for ${epic} to finish releasing` : ''
}

// spawnHoldReason says why an armed drain is starting nothing: a blocker no
// queued item can clear, a repo already running a loop, every run lane busy, a
// pending self-reload, a release, or a drain loop that stopped ticking at all.
// Empty when the drain is not holding, so a queue between children still reads as
// idle rather than stuck.
export function spawnHoldReason(queue?: QueueResponse): string {
  if (!queue?.held) return ''
  return queue.held_reason || 'the hub is holding the next spawn'
}

// laneLabel says how wide a repo's drain is when it runs more than one ticket at
// once, so the loop header reads as N concurrent lanes rather than as one run the
// page happens to be listing several times. Empty for a single-lane repo, which is
// every repo without worktrees and the state the page has always shown.
export function laneLabel(queue?: QueueResponse, running = 0): string {
  const lanes = queue?.lanes ?? 1
  if (lanes < 2) return ''
  return `${running}/${lanes} lanes`
}

// queueTerminal reports whether an item has already settled: the drain only
// launches pending or paused items, so a done, failed, skipped, or awaiting-merge
// one no longer contributes work to a Start.
export function queueTerminal(status: string): boolean {
  return (
    status === 'done' ||
    status === 'failed' ||
    status === 'skipped' ||
    status === 'awaiting-merge'
  )
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

const BATCH_STAMP: Intl.DateTimeFormatOptions = {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
}

// batchDisplayName is how a batch reads on every surface: the name it was filed
// under, or when it was filed for one left unnamed.
export function batchDisplayName(batch: QueueBatch): string {
  return (
    batch.name || new Date(batch.created_at).toLocaleString('en-US', BATCH_STAMP)
  )
}

// batchName labels a batch a surface holds only the id of — a member row's chip,
// or the scope of the drain in flight. Empty when the repo lists no such batch.
export function batchName(batches: QueueBatch[] | undefined, id: string): string {
  const batch = (batches ?? []).find((b) => b.id === id)
  return batch ? batchDisplayName(batch) : ''
}

// batchSelectable reports whether a queued row can join a new batch: the drain
// still has it to launch and no other batch holds it.
export function batchSelectable(item: QueueItem): boolean {
  return queueStatusRunnable(item.status) && !item.batch
}

export interface BatchSummary {
  members: number
  runnable: number
  // tally is how the members that are past pending stand, in queue order. A
  // pending one has no outcome yet and the member count already speaks for it.
  tally: { status: string; count: number }[]
}

// batchSummary is what a batch card says about itself: how much work it holds,
// how much of it a Start would still launch, and what became of the rest.
export function batchSummary(items: QueueItem[], id: string): BatchSummary {
  const members = items.filter((it) => it.batch === id)
  const counts = new Map<string, number>()
  for (const it of members) {
    if (it.status === 'pending') continue
    counts.set(it.status, (counts.get(it.status) ?? 0) + 1)
  }
  return {
    members: members.length,
    runnable: members.filter((it) => queueStatusRunnable(it.status)).length,
    tally: [...counts].map(([status, count]) => ({ status, count })),
  }
}

// batchMembers is what a batch holds, in the order a Start would launch it.
export function batchMembers(items: QueueItem[], id: string): QueueItem[] {
  return items
    .filter((it) => it.batch === id)
    .sort((a, b) => a.position - b.position)
}

// batchStartBlocker says why Start batch is unavailable, mirroring the refusals
// the hub would answer with so the disabled control reads the same as the API.
export function batchStartBlocker(queue: QueueResponse, id: string): string {
  if (queue.draining) {
    return 'the queue is draining — stop it before starting a batch'
  }
  const running = queue.items.find((it) => it.status === 'running')
  if (running) return `${running.id} is already running`
  if (batchSummary(queue.items, id).runnable === 0) {
    return 'nothing left to run in this batch'
  }
  return ''
}
