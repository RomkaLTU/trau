import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Anomaly } from './rundetail'

export interface CostSpend {
  tokens: number
  cost_usd: number
  metered: boolean
}

export interface CostBudget {
  daily_usd?: number
  daily_tokens?: number
}

export interface DailyCost {
  date: string
  tokens: number
  cost_usd: number
  metered: boolean
}

export interface RepoCost {
  repo: string
  tokens: number
  cost_usd: number
  metered: boolean
  daily_budget_usd?: number
  daily_budget_tokens?: number
}

export interface PhaseSpend {
  phase: string
  tokens: number
  cost_usd: number
  metered: boolean
}

export interface CostAnomaly extends Anomaly {
  repo: string
  ticket: string
}

export interface CostsResponse {
  window_days: number
  from: string
  to: string
  totals: CostSpend
  budget: CostBudget
  daily: DailyCost[]
  repos: RepoCost[]
  phases: PhaseSpend[]
  anomalies: CostAnomaly[]
}

async function fetchCosts(days: number): Promise<CostsResponse> {
  const res = await apiFetch(`/api/v1/costs?days=${days}`)
  if (!res.ok) {
    throw new Error(`costs request failed: ${res.status}`)
  }
  return res.json()
}

export const costsQueryOptions = (days: number) =>
  queryOptions({
    queryKey: ['costs', days],
    queryFn: () => fetchCosts(days),
    refetchInterval: 5000,
  })

export type GroupBy = 'repo' | 'provider' | 'model' | 'phase'

export const GROUP_BY: { value: GroupBy; label: string }[] = [
  { value: 'repo', label: 'repo' },
  { value: 'provider', label: 'provider' },
  { value: 'model', label: 'model' },
  { value: 'phase', label: 'phase' },
]

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

export interface TimeseriesParams {
  days?: number
  from?: string
  to?: string
  groupBy: GroupBy
  repos?: string[]
}

function buildQuery(p: TimeseriesParams): string {
  const q = new URLSearchParams()
  if (p.from && p.to) {
    q.set('from', p.from)
    q.set('to', p.to)
  } else if (p.days) {
    q.set('days', String(p.days))
  }
  q.set('group_by', p.groupBy)
  for (const repo of p.repos ?? []) q.append('repo', repo)
  return q.toString()
}

async function fetchTimeseries(
  p: TimeseriesParams,
): Promise<TimeseriesResponse> {
  const res = await apiFetch(`/api/v1/costs/timeseries?${buildQuery(p)}`)
  if (!res.ok) {
    throw new Error(`timeseries request failed: ${res.status}`)
  }
  return res.json()
}

export const timeseriesQueryOptions = (p: TimeseriesParams) =>
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

export const OTHER_KEY = '__other'
export const PREV_KEY = '__prev'
const TOP_N = 8

export type ChartRow = Record<string, number | string>

export interface ChartData {
  rows: ChartRow[]
  keys: string[]
}

export interface GroupTotal {
  key: string
  tokens: number
  cost_usd: number
  metered: boolean
}

// collapseSeries keeps the top N spenders and folds the rest into one "other"
// bucket, so the chart stack and the breakdown table share the same rows.
export function collapseSeries(series: TimeseriesGroup[]): GroupTotal[] {
  const pick = (g: TimeseriesGroup): GroupTotal => ({
    key: g.key,
    tokens: g.tokens,
    cost_usd: g.cost_usd,
    metered: g.metered,
  })
  if (series.length <= TOP_N) return series.map(pick)
  const rest = series.slice(TOP_N)
  return [
    ...series.slice(0, TOP_N).map(pick),
    {
      key: OTHER_KEY,
      tokens: rest.reduce((s, g) => s + g.tokens, 0),
      cost_usd: round2(rest.reduce((s, g) => s + g.cost_usd, 0)),
      metered: rest.every((g) => g.metered),
    },
  ]
}

// toChartRows pivots per-series daily points into one row per day keyed by
// series, collapsing everything past the top N spenders into a single "other"
// bucket so the stack stays legible.
export function toChartRows(series: TimeseriesGroup[]): ChartData {
  if (series.length === 0) return { rows: [], keys: [] }
  const top = series.slice(0, TOP_N)
  const rest = series.slice(TOP_N)
  const dates = series[0].points.map((p) => p.date)
  const rows: ChartRow[] = dates.map((date, i) => {
    const row: ChartRow = { date }
    for (const s of top) row[s.key] = s.points[i]?.cost_usd ?? 0
    if (rest.length > 0) {
      row[OTHER_KEY] = rest.reduce(
        (sum, s) => sum + (s.points[i]?.cost_usd ?? 0),
        0,
      )
    }
    return row
  })
  const keys = top.map((s) => s.key)
  if (rest.length > 0) keys.push(OTHER_KEY)
  return { rows, keys }
}

// priorTotalsByIndex sums a comparison window's series to one spend figure per
// day index, aligned to the current window so it can overlay the chart.
export function priorTotalsByIndex(
  prior: TimeseriesResponse,
  length: number,
): number[] {
  const totals = new Array<number>(length).fill(0)
  for (const s of prior.series) {
    s.points.forEach((p, i) => {
      if (i < length) totals[i] += p.cost_usd
    })
  }
  return totals.map(round2)
}

export interface DeltaRow {
  key: string
  prev: number
  cur: number
  delta: number
  pct: number | null
}

// seriesDelta pairs each group's current and prior window spend, most-changed
// first, for the compare breakdown.
export function seriesDelta(
  current: TimeseriesResponse,
  prior: TimeseriesResponse,
): DeltaRow[] {
  const cur = new Map(current.series.map((s) => [s.key, s.cost_usd]))
  const prev = new Map(prior.series.map((s) => [s.key, s.cost_usd]))
  const rows: DeltaRow[] = []
  for (const key of new Set([...cur.keys(), ...prev.keys()])) {
    const c = cur.get(key) ?? 0
    const p = prev.get(key) ?? 0
    rows.push({
      key,
      prev: p,
      cur: c,
      delta: round2(c - p),
      pct: p > 0 ? ((c - p) / p) * 100 : null,
    })
  }
  rows.sort((a, b) => b.cur - a.cur || b.prev - a.prev)
  return rows
}

export function money(usd: number, metered: boolean): string {
  const formatted = `$${usd.toFixed(2)}`
  return metered ? formatted : `≥ ${formatted}`
}

export function compactUsd(v: number): string {
  if (v >= 1000) return `$${(v / 1000).toFixed(1)}k`
  return `$${v % 1 === 0 ? v : v.toFixed(1)}`
}

function round2(v: number): number {
  return Math.round(v * 100) / 100
}
