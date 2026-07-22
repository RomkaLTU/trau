import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface CohortPhase {
  phase: string
  provider?: string
  model?: string
  effort?: string
  calls: number
  cost_usd: number
  avg_cost_usd: number
  avg_duration_ms: number
  avg_turns: number
  avg_context: number
  metered: boolean
}

export interface ConfigCohort {
  hash: string
  first_seen: string
  last_seen: string
  tickets: number
  calls: number
  cost_usd: number
  metered: boolean
  cost_per_ticket: number
  verify_retry_rate: number
  repair_rate: number
  routing?: Record<string, string>
  phases: CohortPhase[]
}

export interface ConfigCohortsResponse {
  repo: string
  since?: string
  until?: string
  phase?: string
  cohorts: ConfigCohort[]
}

// The hub's bucket for calls logged before it fingerprinted the routing config.
export const UNKNOWN_COHORT = 'unknown'

export const LOW_SAMPLE_TICKETS = 5

async function fetchConfigCohorts(
  repo: string,
): Promise<ConfigCohortsResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/metrics/config-cohorts`,
  )
  if (!res.ok) {
    throw new Error(`config cohorts request failed: ${res.status}`)
  }
  return res.json()
}

export const configCohortsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['config-cohorts', repo],
    queryFn: () => fetchConfigCohorts(repo),
    enabled: repo !== '',
  })

export function orderCohorts(cohorts: ConfigCohort[]): ConfigCohort[] {
  return [
    ...cohorts.filter((c) => c.hash !== UNKNOWN_COHORT),
    ...cohorts.filter((c) => c.hash === UNKNOWN_COHORT),
  ]
}

export function isLegacy(cohort: ConfigCohort): boolean {
  return cohort.hash === UNKNOWN_COHORT
}

export function isLowSample(cohort: ConfigCohort): boolean {
  return cohort.tickets < LOW_SAMPLE_TICKETS
}

export function cohortLabel(cohort: ConfigCohort): string {
  return isLegacy(cohort) ? 'unknown (legacy)' : cohort.hash
}

export interface RoutingChange {
  key: string
  from: string
  to: string
}

// Returns null when either fingerprint is unresolved, so "nothing changed" stays
// distinguishable from "the hub can't say".
export function routingDiff(
  to: Record<string, string> | undefined,
  from: Record<string, string> | undefined,
): RoutingChange[] | null {
  if (!to || !from) return null
  const keys = [...new Set([...Object.keys(from), ...Object.keys(to)])].sort()
  const changes: RoutingChange[] = []
  for (const key of keys) {
    const before = from[key] ?? ''
    const after = to[key] ?? ''
    if (before !== after) changes.push({ key, from: before, to: after })
  }
  return changes
}

export interface Delta {
  cur: number
  prev: number
  delta: number
  pct: number | null
}

export function deltaOf(cur: number, prev: number): Delta {
  return {
    cur,
    prev,
    delta: round4(cur - prev),
    pct: prev > 0 ? ((cur - prev) / prev) * 100 : null,
  }
}

export interface PhaseComparison {
  phase: string
  route: string
  baselineRoute: string
  calls: number
  cost: Delta
  duration: Delta
  turns: Delta
}

// A phase one side never ran compares against zero — it did cost that side nothing.
export function comparePhases(
  current: ConfigCohort,
  baseline: ConfigCohort,
): PhaseComparison[] {
  const cur = new Map(current.phases.map((p) => [p.phase, p]))
  const prev = new Map(baseline.phases.map((p) => [p.phase, p]))
  const names = new Set([...cur.keys(), ...prev.keys()])

  return [...names].map((phase) => {
    const c = cur.get(phase)
    const p = prev.get(phase)
    return {
      phase,
      route: routeLabel(c),
      baselineRoute: routeLabel(p),
      calls: c?.calls ?? 0,
      cost: deltaOf(c?.avg_cost_usd ?? 0, p?.avg_cost_usd ?? 0),
      duration: deltaOf(c?.avg_duration_ms ?? 0, p?.avg_duration_ms ?? 0),
      turns: deltaOf(c?.avg_turns ?? 0, p?.avg_turns ?? 0),
    }
  })
}

export function routeLabel(phase: CohortPhase | undefined): string {
  if (!phase) return ''
  return [phase.provider, phase.model, phase.effort].filter(Boolean).join(':')
}

export function cohortWindow(cohort: ConfigCohort): string {
  const from = shortDate(cohort.first_seen)
  const to = shortDate(cohort.last_seen)
  return from === to ? from : `${from} → ${to}`
}

function shortDate(ts: string): string {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

// Three decimals below a dollar: per-call phase averages live there, and two
// would round most of the difference away.
export function avgUsd(usd: number): string {
  return `$${usd.toFixed(Math.abs(usd) < 1 ? 3 : 2)}`
}

export function ratePct(rate: number): string {
  return `${(rate * 100).toFixed(0)}%`
}

export function durationLabel(ms: number): string {
  const seconds = ms / 1000
  return Math.abs(seconds) < 60
    ? `${seconds.toFixed(1)}s`
    : `${(seconds / 60).toFixed(1)}m`
}

export function signed(value: number, format: (v: number) => string): string {
  return `${value > 0 ? '+' : value < 0 ? '-' : ''}${format(Math.abs(value))}`
}

export function pctLabel(pct: number | null): string {
  if (pct === null) return ''
  return `${pct > 0 ? '+' : pct < 0 ? '-' : ''}${Math.abs(pct).toFixed(0)}%`
}

function round4(v: number): number {
  return Math.round(v * 10000) / 10000
}
