import { useQuery } from '@tanstack/react-query'

import type { RunState } from '@/components/trau/status-pill'
import { costsQueryOptions } from './costs'
import { instancesQueryOptions } from './instances'
import { runsQueryOptions } from './runs'

const PHASE_RANK: Record<string, number> = {
  building: 1,
  built: 2,
  handed_off: 3,
  verified: 4,
  pr_open: 5,
  merged: 6,
}

export function phaseRank(phase: string): number {
  return PHASE_RANK[phase] ?? 0
}

const PHASE_SEQUENCE: { label: string; min: number; max: number }[] = [
  { label: 'build', min: 1, max: 2 },
  { label: 'handoff', min: 3, max: 3 },
  { label: 'verify', min: 4, max: 4 },
  { label: 'pr', min: 5, max: 5 },
  { label: 'merge', min: 6, max: 6 },
]

export type PhaseState = 'done' | 'active' | 'todo'

export interface PhaseStep {
  label: string
  state: PhaseState
}

export function phaseSteps(phase: string): PhaseStep[] {
  const rank = phaseRank(phase)
  return PHASE_SEQUENCE.map(({ label, min, max }) => ({
    label,
    state: rank > max ? 'done' : rank >= min ? 'active' : 'todo',
  }))
}

export function phasePill(phase: string): { state: RunState; label: string } {
  switch (phase) {
    case 'building':
    case 'built':
      return { state: 'active', label: 'build' }
    case 'handed_off':
      return { state: 'active', label: 'handoff' }
    case 'verified':
      return { state: 'verify', label: 'verify' }
    case 'pr_open':
      return { state: 'info', label: 'pr' }
    case 'merged':
      return { state: 'success', label: 'merged' }
    default:
      return { state: 'active', label: phase || 'running' }
  }
}

export interface LiveLoop {
  repo: string
  pid: number
  ticket?: string
  title?: string
  phase: string
  startedAt: string
  phaseSince?: string
}

export function useLiveLoops(repo: string | null): LiveLoop[] {
  const { data } = useQuery(instancesQueryOptions)
  const instances = (data?.instances ?? []).filter((i) => i.repo === repo)

  const { data: runs } = useQuery(runsQueryOptions(repo ?? ''))
  const titles = new Map<string, string>()
  for (const run of runs?.runs ?? []) {
    if (run.title) titles.set(run.ticket, run.title)
  }

  return instances.map((inst) => ({
    repo: inst.repo,
    pid: inst.pid,
    ticket: inst.ticket,
    title: inst.ticket ? titles.get(inst.ticket) : undefined,
    phase: inst.session_state === 'working' ? (inst.phase ?? '') : '',
    startedAt: inst.started_at,
    phaseSince: inst.state_since,
  }))
}

export interface TodaySpend {
  cost: number
  budget?: number
  metered: boolean
}

export function useTodaySpend(repo: string | null): TodaySpend {
  const { data } = useQuery(costsQueryOptions(1))
  const scoped = data?.repos.find((r) => r.repo === repo)
  return {
    cost: scoped?.cost_usd ?? 0,
    budget: scoped?.daily_budget_usd ?? data?.budget.daily_usd,
    metered: scoped?.metered ?? true,
  }
}
