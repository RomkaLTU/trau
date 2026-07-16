import { describe, expect, it } from 'vitest'

import { deriveLoopHalt, loopView } from '@/lib/loop'
import type { FeedEvent } from '@/lib/events'
import type { Instance } from '@/lib/instances'

function stateChange(
  id: string,
  state: string,
  fields: Record<string, unknown> = {},
): FeedEvent {
  return { id, ts: '', kind: 'state_change', fields: { state, ...fields } }
}

function instance(over: Partial<Instance>): Instance {
  return {
    pid: 1,
    repo: 'loop',
    repo_root: '/loop',
    runs_dir: 'runs',
    started_at: '2026-07-16T10:00:00Z',
    session_state: 'working',
    ...over,
  }
}

describe('loopView', () => {
  it('shows the running view while the queue drains', () => {
    expect(loopView(true)).toBe('running')
  })

  it('keeps the running view up for an active instance after the drain flag drops', () => {
    expect(loopView(false, instance({ ticket: 'COD-1' }))).toBe('running')
    expect(
      loopView(false, instance({ ticket: 'COD-1', session_state: 'stopping' })),
    ).toBe('running')
  })

  it('returns to the builder once the instance goes idle', () => {
    expect(loopView(false)).toBe('builder')
    expect(
      loopView(false, instance({ ticket: 'COD-1', session_state: 'idle' })),
    ).toBe('builder')
  })

  it('leaves a parked instance to the halt banner, not the running view', () => {
    expect(
      loopView(false, instance({ ticket: 'COD-1', session_state: 'parked' })),
    ).toBe('builder')
  })

  it('ignores an active instance that carries no ticket', () => {
    expect(loopView(false, instance({}))).toBe('builder')
  })
})

describe('deriveLoopHalt', () => {
  it('returns null with no events', () => {
    expect(deriveLoopHalt([])).toBeNull()
  })

  it('classifies a rate-limit pause', () => {
    const events = [
      stateChange('3', 'paused', { ticket: 'COD-1', reason: 'usage_window' }),
    ]
    expect(deriveLoopHalt(events)).toEqual({
      kind: 'paused',
      ticket: 'COD-1',
      reason: 'usage_window',
    })
  })

  it('classifies a budget give-up as budget, not a bare quarantine', () => {
    const events = [
      stateChange('5', 'quarantined', {
        ticket: 'COD-2',
        reason: 'budget cap reached — daily ≤ $5',
      }),
    ]
    expect(deriveLoopHalt(events)?.kind).toBe('budget')
  })

  it('classifies a non-budget quarantine', () => {
    const events = [
      stateChange('5', 'quarantined', {
        ticket: 'COD-2',
        reason: 'needs human',
      }),
    ]
    expect(deriveLoopHalt(events)?.kind).toBe('quarantined')
  })

  it('classifies a fault', () => {
    const events = [stateChange('2', 'faulted', { ticket: 'COD-3' })]
    expect(deriveLoopHalt(events)?.kind).toBe('fault')
  })

  it('reads a clean merge as not halted', () => {
    expect(deriveLoopHalt([stateChange('9', 'merged')])).toBeNull()
  })

  it('honors feed ordering — the newest terminal event wins', () => {
    const events = [
      stateChange('4', 'paused', { reason: 'usage_window' }),
      stateChange('2', 'faulted'),
    ]
    expect(deriveLoopHalt(events)?.kind).toBe('paused')
  })

  it('ignores non-terminal noise between terminal events', () => {
    const events = [
      { id: '6', ts: '', kind: 'agent_call', fields: {} } as FeedEvent,
      stateChange('3', 'faulted', { ticket: 'COD-7' }),
    ]
    expect(deriveLoopHalt(events)?.kind).toBe('fault')
  })
})
