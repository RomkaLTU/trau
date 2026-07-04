import { queryOptions } from '@tanstack/react-query'

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
  const res = await fetch(`/api/v1/costs?days=${days}`)
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
