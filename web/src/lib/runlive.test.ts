import { describe, expect, it } from 'vitest'

import type { PhaseCost } from '@/lib/rundetail'
import {
  deriveElapsedMs,
  deriveVariant,
  formatCostUSD,
  formatDuration,
  formatTokens,
  headerPill,
  pauseBanner,
  pauseKind,
  phaseLabel,
  runPhaseSteps,
  sumCosts,
} from '@/lib/runlive'

describe('deriveVariant', () => {
  it('is live only while an instance is working the ticket', () => {
    expect(deriveVariant({ phase: 'merged', working: true })).toBe('live')
    expect(deriveVariant({ phase: 'verified', failureClass: 'paused', working: true })).toBe('live')
  })

  it('reads a stopped run from its checkpoint', () => {
    expect(deriveVariant({ phase: 'merged', working: false })).toBe('success')
    expect(deriveVariant({ phase: 'verified', failureClass: 'paused', working: false })).toBe('paused')
    expect(deriveVariant({ phase: 'built', failureClass: 'faulted', working: false })).toBe('failure')
    expect(deriveVariant({ phase: 'quarantined', failureClass: 'gave_up', working: false })).toBe('failure')
  })

  it('reads a stopped in-flight run with a checkpoint or instance as live', () => {
    expect(deriveVariant({ phase: 'building', working: false, hasCheckpoint: true })).toBe('live')
    expect(deriveVariant({ phase: 'built', working: false, live: true })).toBe('live')
  })

  it('is starting before a checkpoint or instance exists', () => {
    expect(deriveVariant({ phase: '', working: false })).toBe('starting')
    expect(deriveVariant({ phase: 'building', working: false })).toBe('starting')
  })

  it('is failed_to_start when the child died before landing', () => {
    expect(deriveVariant({ phase: '', working: false, spawnFailed: true })).toBe('failed_to_start')
  })

  it('never lets a stale spawn failure override a run that then landed', () => {
    expect(deriveVariant({ phase: '', working: false, spawnFailed: true, live: true })).toBe('live')
    expect(
      deriveVariant({ phase: 'building', working: false, spawnFailed: true, hasCheckpoint: true }),
    ).toBe('live')
    expect(deriveVariant({ phase: 'merged', working: false, spawnFailed: true })).toBe('success')
  })
})

describe('runPhaseSteps', () => {
  const states = (phase: string, variant: Parameters<typeof runPhaseSteps>[1]) =>
    Object.fromEntries(runPhaseSteps(phase, variant).map((s) => [s.label, s.state]))

  it('marks the reached phase active while live', () => {
    expect(states('handed_off', 'live')).toEqual({
      build: 'done',
      handoff: 'active',
      verify: 'todo',
      pr: 'todo',
      merge: 'todo',
    })
  })

  it('completes every step on success', () => {
    expect(new Set(runPhaseSteps('merged', 'success').map((s) => s.state))).toEqual(
      new Set(['done']),
    )
  })

  it('turns the reached phase into a fail marker on failure', () => {
    expect(states('handed_off', 'failure')).toEqual({
      build: 'done',
      handoff: 'fail',
      verify: 'todo',
      pr: 'todo',
      merge: 'todo',
    })
  })
})

describe('headerPill', () => {
  it('labels each terminal variant', () => {
    expect(headerPill('success', 'merged')).toEqual({ state: 'success', label: 'merged' })
    expect(headerPill('paused', 'verified')).toEqual({ state: 'warn', label: 'paused' })
    expect(headerPill('failure', 'built', 'faulted')).toEqual({ state: 'fail', label: 'fault' })
    expect(headerPill('failure', 'quarantined', 'gave_up')).toEqual({
      state: 'fail',
      label: 'quarantined',
    })
  })

  it('derives a live pill from the phase', () => {
    expect(headerPill('live', 'building')).toEqual({ state: 'active', label: 'build' })
  })

  it('labels the launch and dead-on-arrival states', () => {
    expect(headerPill('starting', '')).toEqual({ state: 'active', label: 'starting' })
    expect(headerPill('failed_to_start', '')).toEqual({
      state: 'fail',
      label: 'failed to start',
    })
  })
})

describe('phaseLabel', () => {
  it('reads checkpoint phases as pipeline steps', () => {
    expect(phaseLabel('merged')).toBe('merge')
    expect(phaseLabel('pr_open')).toBe('pr')
    expect(phaseLabel('handed_off')).toBe('handoff')
    expect(phaseLabel('')).toBe('queued')
  })
})

describe('pauseKind / pauseBanner', () => {
  it('tells an auth wall from a usage limit', () => {
    expect(pauseKind('claude authentication required — re-login')).toBe('reauth')
    expect(pauseKind('claude rate/usage limit reached')).toBe('usage_window')
    expect(pauseKind('something else')).toBe('other')
  })

  it('reads blameless with the provider and a next step', () => {
    const reauth = pauseBanner('claude authentication required — re-login')
    expect(reauth.headline).toBe('paused — claude needs re-authentication')
    expect(reauth.hint).toContain('Re-login')

    const usage = pauseBanner('kimi rate/usage limit reached')
    expect(usage.headline).toBe('paused — kimi usage limit reached')
    expect(usage.hint).toContain('resets')
  })
})

describe('cost + token formatting', () => {
  const costs: PhaseCost[] = [
    phaseCost({ total: 700_000, cost_usd: 1.2, metered: true }),
    phaseCost({ total: 500_000, cost_usd: 0.74, metered: true }),
  ]

  it('sums totals and keeps metered only when every phase is metered', () => {
    expect(sumCosts(costs)).toEqual({ tokens: 1_200_000, usd: 1.94, metered: true })
    expect(sumCosts([phaseCost({ metered: false, cost_usd: 1, total: 10 })]).metered).toBe(false)
  })

  it('formats tokens compactly', () => {
    expect(formatTokens(1_200_000)).toBe('1.2M')
    expect(formatTokens(700_000)).toBe('700K')
    expect(formatTokens(512)).toBe('512')
  })

  it('marks an unmetered cost as a lower bound', () => {
    expect(formatCostUSD(3.8, true)).toBe('$3.80')
    expect(formatCostUSD(3.8, false)).toBe('≥ $3.80')
  })
})

describe('deriveElapsedMs', () => {
  it('spans a run from the previous terminal event to its own', () => {
    const events = [
      ev('2026-07-05T14:00:00Z', 'state_change', { state: 'merged', ticket: 'COD-1' }),
      ev('2026-07-05T14:00:10Z', 'agent_start', {}),
      ev('2026-07-05T14:02:00Z', 'agent_call', {}),
      ev('2026-07-05T14:18:52Z', 'state_change', { state: 'merged', ticket: 'COD-2' }),
    ]
    expect(deriveElapsedMs(events, 'COD-2')).toBe((18 * 60 + 42) * 1000)
    expect(formatDuration(deriveElapsedMs(events, 'COD-2')!)).toBe('18m 42s')
  })

  it('is null when the feed lacks the run terminal event', () => {
    const events = [ev('2026-07-05T14:00:10Z', 'agent_start', {})]
    expect(deriveElapsedMs(events, 'COD-2')).toBeNull()
  })
})

function phaseCost(partial: Partial<PhaseCost>): PhaseCost {
  return {
    phase: 'build',
    input: 0,
    output: 0,
    cache_read: 0,
    cache_creation: 0,
    reasoning: 0,
    total: 0,
    cost_usd: 0,
    metered: true,
    calls: 0,
    turns: 0,
    ...partial,
  }
}

function ev(ts: string, kind: string, fields: Record<string, unknown>) {
  return { ts, kind, fields }
}
