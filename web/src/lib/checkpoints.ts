import { apiFetch } from './api'

export interface ResetResult {
  status: string
  ticket: string
}

export interface ClearResult {
  status: string
  ticket: string
  was: string
}

export interface ReconcileResult {
  repo: string
  reconciled: string[]
}

// CheckpointError carries the machine-readable flags the run views branch on: a
// live-instance refusal, and the merged-ticket case the UI escalates to a forced
// confirmation.
export class CheckpointError extends Error {
  live: boolean
  requiresForce: boolean

  constructor(message: string, opts: { live?: boolean; requiresForce?: boolean }) {
    super(message)
    this.name = 'CheckpointError'
    this.live = opts.live ?? false
    this.requiresForce = opts.requiresForce ?? false
  }
}

async function post<T>(url: string, body?: unknown): Promise<T> {
  const res = await apiFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
      live?: boolean
      requires_force?: boolean
    } | null
    throw new CheckpointError(detail?.error ?? `request failed: ${res.status}`, {
      live: detail?.live,
      requiresForce: detail?.requires_force,
    })
  }
  return res.json()
}

export function resetRun(
  repo: string,
  ticket: string,
  force: boolean,
): Promise<ResetResult> {
  return post(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/reset`,
    { force },
  )
}

export function clearRun(repo: string, ticket: string): Promise<ClearResult> {
  return post(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(
      ticket,
    )}/clear`,
  )
}

export function reconcileRepo(repo: string): Promise<ReconcileResult> {
  return post(`/api/v1/repos/${encodeURIComponent(repo)}/reconcile`)
}
