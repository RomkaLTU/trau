import { describe, expect, it } from 'vitest'

import { phasePill, phaseRank, phaseSteps, type PhaseState } from '@/lib/overview'

function states(phase: string): Record<string, PhaseState> {
  return Object.fromEntries(phaseSteps(phase).map((s) => [s.label, s.state]))
}

describe('phaseRank', () => {
  it('ranks the checkpoint pipeline in order', () => {
    expect(phaseRank('building')).toBe(1)
    expect(phaseRank('built')).toBe(2)
    expect(phaseRank('handed_off')).toBe(3)
    expect(phaseRank('verified')).toBe(4)
    expect(phaseRank('pr_open')).toBe(5)
    expect(phaseRank('merged')).toBe(6)
  })

  it('treats unknown/empty phases as rank 0', () => {
    expect(phaseRank('')).toBe(0)
    expect(phaseRank('quarantined')).toBe(0)
  })
})

describe('phaseSteps', () => {
  it('marks the current phase active and prior phases done', () => {
    expect(states('handed_off')).toEqual({
      build: 'done',
      handoff: 'active',
      verify: 'todo',
      pr: 'todo',
      merge: 'todo',
    })
  })

  it('groups building and built under a single active build step', () => {
    expect(states('building').build).toBe('active')
    expect(states('built').build).toBe('active')
  })

  it('completes every prior step once merged', () => {
    expect(states('pr_open')).toEqual({
      build: 'done',
      handoff: 'done',
      verify: 'done',
      pr: 'active',
      merge: 'todo',
    })
    expect(states('merged').merge).toBe('active')
  })

  it('leaves every step todo for an unknown phase', () => {
    expect(new Set(phaseSteps('').map((s) => s.state))).toEqual(new Set(['todo']))
  })
})

describe('phasePill', () => {
  it('maps checkpoint phases to run-state pills', () => {
    expect(phasePill('building')).toEqual({ state: 'active', label: 'build' })
    expect(phasePill('handed_off')).toEqual({ state: 'active', label: 'handoff' })
    expect(phasePill('verified')).toEqual({ state: 'verify', label: 'verify' })
    expect(phasePill('pr_open')).toEqual({ state: 'info', label: 'pr' })
    expect(phasePill('merged')).toEqual({ state: 'success', label: 'merged' })
  })

  it('falls back to the raw phase for anything unmapped', () => {
    expect(phasePill('picking')).toEqual({ state: 'active', label: 'picking' })
    expect(phasePill('')).toEqual({ state: 'active', label: 'running' })
  })
})
