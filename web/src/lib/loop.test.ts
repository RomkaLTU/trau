import { describe, expect, it } from 'vitest'

import { deriveLoopHalt } from '@/lib/loop'
import type { FeedEvent } from '@/lib/events'

function stateChange(
  id: string,
  state: string,
  fields: Record<string, unknown> = {},
): FeedEvent {
  return { id, ts: '', kind: 'state_change', fields: { state, ...fields } }
}

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
