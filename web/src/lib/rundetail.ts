import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Run } from './runs'

export interface PhaseCost {
  phase: string
  input: number
  output: number
  cache_read: number
  cache_creation: number
  reasoning: number
  total: number
  cost_usd: number
  metered: boolean
  calls: number
  turns: number
}

export interface StepDuration {
  step: string
  duration_ms: number
}

export interface Rubric {
  acceptance_criteria?: string[]
  non_goals?: string[]
  required_tests?: string[]
  ui_paths?: string[]
  fail_conditions?: string[]
}

export interface VerdictCheck {
  name: string
  severity?: string
  pass: boolean
  detail?: string
}

export interface Verdict {
  pass: boolean
  summary?: string
  failures?: string[]
  checks?: VerdictCheck[]
  browser?: string
  browser_notes?: string
}

export interface Proof {
  seq: number
  kind: string
  mime?: string
  caption?: string
  trace_dir?: string
  created_at?: string
  is_image: boolean
  url?: string
  trace_exists?: boolean
  render?: '' | 'rendering' | 'failed' | 'done'
  render_error?: string
}

export interface Artifacts {
  handoff: boolean
  rubric: boolean
  verdict: boolean
  build_notes: boolean
  tokens: boolean
}

export interface Anomaly {
  phase: string
  output: number
  turns: number
  cost_usd: number
  reasons: string[]
}

/** The per-ticket git worktree a run works in when the repo has WORKTREES=1. */
export interface Worktree {
  id: number
  repo: string
  ticket: string
  path: string
  branch: string
  /** active while the tree is on disk, settled once removed, orphaned when its directory vanished. */
  state: 'active' | 'settled' | 'orphaned'
  created_at?: string
}

export interface RunDetail extends Run {
  costs: PhaseCost[]
  durations?: StepDuration[]
  anomalies?: Anomaly[]
  handoff?: string
  rubric?: Rubric
  verdict?: Verdict
  build_notes?: string
  artifacts: Artifacts
  no_skills?: boolean
  no_browser?: boolean
  removed?: boolean
  worktree?: Worktree
}

async function fetchRunDetail(
  repo: string,
  ticket: string,
): Promise<RunDetail> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(ticket)}`,
  )
  if (!res.ok) {
    throw new Error(`run detail request failed: ${res.status}`)
  }
  return res.json()
}

export const runDetailQueryOptions = (repo: string, ticket: string) =>
  queryOptions({
    queryKey: ['run', repo, ticket],
    queryFn: () => fetchRunDetail(repo, ticket),
    refetchInterval: 3000,
    refetchIntervalInBackground: true,
    enabled: repo !== '' && ticket !== '',
  })

async function fetchRunProofs(repo: string, ticket: string): Promise<Proof[]> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(ticket)}/proofs`,
  )
  if (!res.ok) {
    throw new Error(`run proofs request failed: ${res.status}`)
  }
  return res.json()
}

export const runProofsQueryOptions = (repo: string, ticket: string) =>
  queryOptions({
    queryKey: ['run-proofs', repo, ticket],
    queryFn: () => fetchRunProofs(repo, ticket),
    refetchInterval: 5000,
    enabled: repo !== '' && ticket !== '',
  })

export async function renderRunVideo(
  repo: string,
  ticket: string,
): Promise<void> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(ticket)}/render-video`,
    { method: 'POST' },
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `render video failed: ${res.status}`)
  }
}

export interface RunRequeueResult {
  ticket: string
  changed: string[]
}

export async function requeueRun(
  repo: string,
  ticket: string,
): Promise<RunRequeueResult> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/runs/${encodeURIComponent(ticket)}/requeue`,
    { method: 'POST' },
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `requeue failed: ${res.status}`)
  }
  return res.json()
}

/**
 * Removes a run's worktree from disk and settles its row. The hub refuses while a
 * run is still live in the repo, which surfaces here as that refusal's own message.
 */
export async function removeWorktree(
  repo: string,
  id: number,
): Promise<Worktree> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/worktrees/${id}`,
    { method: 'DELETE' },
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `remove worktree failed: ${res.status}`)
  }
  return res.json()
}
