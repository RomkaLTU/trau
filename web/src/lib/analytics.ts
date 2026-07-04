import { queryOptions } from '@tanstack/react-query'

import type { CostSpend } from './costs'

export type GroupBy = 'provider' | 'repo' | 'model' | 'phase'

export interface TimeseriesPoint {
  date: string
  tokens: number
  cost_usd: number
}

export interface TimeseriesGroup {
  key: string
  tokens: number
  cost_usd: number
  metered: boolean
  points: TimeseriesPoint[]
}

export interface CostFacets {
  repos: string[]
  providers: string[]
  models: string[]
  phases: string[]
}

export interface TimeseriesResponse {
  from: string
  to: string
  days: number
  group_by: GroupBy
  totals: CostSpend
  series: TimeseriesGroup[]
  facets: CostFacets
}

export interface AnalyticsParams {
  days?: number
  from?: string
  to?: string
  groupBy: GroupBy
  repos?: string[]
  providers?: string[]
  models?: string[]
  phases?: string[]
}

function buildQuery(p: AnalyticsParams): string {
  const q = new URLSearchParams()
  if (p.from && p.to) {
    q.set('from', p.from)
    q.set('to', p.to)
  } else if (p.days) {
    q.set('days', String(p.days))
  }
  q.set('group_by', p.groupBy)
  for (const repo of p.repos ?? []) q.append('repo', repo)
  for (const provider of p.providers ?? []) q.append('provider', provider)
  for (const model of p.models ?? []) q.append('model', model)
  for (const phase of p.phases ?? []) q.append('phase', phase)
  return q.toString()
}

async function fetchTimeseries(p: AnalyticsParams): Promise<TimeseriesResponse> {
  const res = await fetch(`/api/v1/costs/timeseries?${buildQuery(p)}`)
  if (!res.ok) {
    throw new Error(`timeseries request failed: ${res.status}`)
  }
  return res.json()
}

export const timeseriesQueryOptions = (p: AnalyticsParams) =>
  queryOptions({
    queryKey: ['timeseries', p],
    queryFn: () => fetchTimeseries(p),
    refetchInterval: 5000,
  })

// previousWindow returns the from/to of the equal-length period immediately
// before a trailing days-long window ending today — the "before" side of a
// before/after comparison. Dates are local to match the server's day boundaries.
export function previousWindow(days: number): { from: string; to: string } {
  return { from: localDay(-(2 * days - 1)), to: localDay(-days) }
}

function localDay(offset: number): string {
  const d = new Date()
  d.setDate(d.getDate() + offset)
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}
