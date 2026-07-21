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
