import { describe, expect, it } from 'vitest'

import {
  UNKNOWN_COHORT,
  avgUsd,
  cohortLabel,
  comparePhases,
  deltaOf,
  durationLabel,
  isLowSample,
  orderCohorts,
  pctLabel,
  ratePct,
  routingDiff,
  signed,
  type CohortPhase,
  type ConfigCohort,
} from '@/lib/cohorts'

function phase(partial: Partial<CohortPhase> & { phase: string }): CohortPhase {
  return {
    calls: 1,
    cost_usd: 0,
    avg_cost_usd: 0,
    avg_duration_ms: 0,
    avg_turns: 0,
    avg_context: 0,
    metered: true,
    ...partial,
  }
}

function cohort(partial: Partial<ConfigCohort> & { hash: string }): ConfigCohort {
  return {
    first_seen: '2026-07-01T10:00:00',
    last_seen: '2026-07-02T10:00:00',
    tickets: 10,
    calls: 40,
    cost_usd: 0,
    metered: true,
    cost_per_ticket: 0,
    verify_retry_rate: 0,
    repair_rate: 0,
    phases: [],
    ...partial,
  }
}

describe('orderCohorts', () => {
  it('sinks the legacy cohort to the end and keeps the rest newest first', () => {
    const ordered = orderCohorts([
      cohort({ hash: 'cfg-b' }),
      cohort({ hash: UNKNOWN_COHORT }),
      cohort({ hash: 'cfg-a' }),
    ])
    expect(ordered.map((c) => c.hash)).toEqual([
      'cfg-b',
      'cfg-a',
      UNKNOWN_COHORT,
    ])
  })
})

describe('routingDiff', () => {
  it('names only the keys that changed, in key order', () => {
    const changes = routingDiff(
      {
        PROVIDER: 'claude',
        PHASE_VERIFY: 'claude:opus:high',
        PHASE_BUILD: 'claude:opus:xhigh',
      },
      {
        PROVIDER: 'claude',
        PHASE_VERIFY: 'claude:opus:xhigh',
        PHASE_BUILD: 'claude:opus:xhigh',
      },
    )
    expect(changes).toEqual([
      { key: 'PHASE_VERIFY', from: 'claude:opus:xhigh', to: 'claude:opus:high' },
    ])
  })

  it('reports keys added or dropped by the new fingerprint', () => {
    expect(routingDiff({ REQUIRED_SKILLS: 'web-feature' }, {})).toEqual([
      { key: 'REQUIRED_SKILLS', from: '', to: 'web-feature' },
    ])
    expect(routingDiff({}, { REQUIRED_SKILLS: 'web-feature' })).toEqual([
      { key: 'REQUIRED_SKILLS', from: 'web-feature', to: '' },
    ])
  })

  it('distinguishes an unresolved fingerprint from an unchanged one', () => {
    expect(routingDiff(undefined, { PROVIDER: 'claude' })).toBeNull()
    expect(routingDiff({ PROVIDER: 'claude' }, undefined)).toBeNull()
    expect(routingDiff({ PROVIDER: 'claude' }, { PROVIDER: 'claude' })).toEqual([])
  })
})

describe('comparePhases', () => {
  const current = cohort({
    hash: 'cfg-b',
    phases: [
      phase({
        phase: 'build',
        provider: 'claude',
        model: 'opus',
        effort: 'xhigh',
        calls: 4,
        avg_cost_usd: 1.5,
        avg_duration_ms: 120000,
        avg_turns: 12,
      }),
      phase({
        phase: 'verify',
        provider: 'claude',
        model: 'opus',
        effort: 'high',
        calls: 2,
        avg_cost_usd: 0.4,
        avg_duration_ms: 40000,
        avg_turns: 5,
      }),
    ],
  })
  const baseline = cohort({
    hash: 'cfg-a',
    phases: [
      phase({
        phase: 'verify',
        provider: 'claude',
        model: 'opus',
        effort: 'xhigh',
        calls: 3,
        avg_cost_usd: 0.5,
        avg_duration_ms: 60000,
        avg_turns: 8,
      }),
      phase({ phase: 'commit', avg_cost_usd: 0.2 }),
    ],
  })

  it('keeps the current cohort order and appends baseline-only phases', () => {
    expect(comparePhases(current, baseline).map((r) => r.phase)).toEqual([
      'build',
      'verify',
      'commit',
    ])
  })

  it('deltas each metric against the baseline', () => {
    const verify = comparePhases(current, baseline)[1]
    expect(verify.cost).toMatchObject({ cur: 0.4, prev: 0.5, delta: -0.1 })
    expect(verify.cost.pct).toBeCloseTo(-20)
    expect(verify.duration.delta).toBe(-20000)
    expect(verify.turns.delta).toBe(-3)
    expect(verify.baselineRoute).toBe('claude:opus:xhigh')
    expect(verify.route).toBe('claude:opus:high')
  })

  it('compares a phase only one cohort ran against zero', () => {
    const rows = comparePhases(current, baseline)
    expect(rows[0].cost).toMatchObject({ cur: 1.5, prev: 0, delta: 1.5, pct: null })
    expect(rows[2].cost).toMatchObject({ cur: 0, prev: 0.2, delta: -0.2 })
    expect(rows[2].calls).toBe(0)
  })
})

describe('formatting', () => {
  it('keeps three decimals on sub-dollar averages', () => {
    expect(avgUsd(0.0125)).toBe('$0.013')
    expect(avgUsd(12.345)).toBe('$12.35')
  })

  it('renders rates, durations and signs', () => {
    expect(ratePct(0.125)).toBe('13%')
    expect(durationLabel(45000)).toBe('45.0s')
    expect(durationLabel(150000)).toBe('2.5m')
    expect(signed(-0.1, avgUsd)).toBe('-$0.100')
    expect(signed(0, avgUsd)).toBe('$0.000')
    expect(pctLabel(-20)).toBe('-20%')
    expect(pctLabel(null)).toBe('')
  })

  it('labels and flags cohorts', () => {
    expect(cohortLabel(cohort({ hash: UNKNOWN_COHORT }))).toBe('unknown (legacy)')
    expect(cohortLabel(cohort({ hash: 'cfg-a' }))).toBe('cfg-a')
    expect(isLowSample(cohort({ hash: 'cfg-a', tickets: 4 }))).toBe(true)
    expect(isLowSample(cohort({ hash: 'cfg-a', tickets: 5 }))).toBe(false)
  })

  it('leaves percent undefined when the baseline is zero', () => {
    expect(deltaOf(3, 0).pct).toBeNull()
  })
})
