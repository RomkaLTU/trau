import { describe, expect, it } from 'vitest'

import type { FeedEvent } from './events'
import {
  steerDeliveryModes,
  steerSettled,
  steerStatusLabel,
  type SteerNote,
} from './steer'

function note(over: Partial<SteerNote> = {}): SteerNote {
  return {
    id: 1,
    ticket: 'COD-1',
    body: 'use the other endpoint',
    status: 'pending',
    ...over,
  }
}

function delivered(id: number, midPhase: boolean): FeedEvent {
  return {
    id: String(id),
    ts: '2026-07-23T10:00:00Z',
    kind: 'steer.delivered',
    fields: { note_id: id, mid_phase: midPhase },
  }
}

describe('steerSettled', () => {
  it('settles a merged run and every failure the hub sweeps notes on', () => {
    expect(steerSettled({ phase: 'merged' })).toBe(true)
    expect(steerSettled({ phase: 'verify', failure_class: 'faulted' })).toBe(true)
    expect(steerSettled({ phase: 'build', failure_class: 'gave_up' })).toBe(true)
  })

  it('keeps a paused or still-running run open to notes', () => {
    expect(steerSettled({ phase: 'build', failure_class: 'paused' })).toBe(false)
    expect(steerSettled({ phase: 'verify' })).toBe(false)
    expect(steerSettled(undefined)).toBe(false)
  })
})

describe('steerDeliveryModes', () => {
  it('separates notes typed into a live session from next-spawn deliveries', () => {
    const modes = steerDeliveryModes([delivered(7, true), delivered(8, false)])
    expect(modes.get(7)).toBe(true)
    expect(modes.get(8)).toBe(false)
  })

  it('ignores events that are not steer deliveries', () => {
    const other: FeedEvent = {
      id: '9',
      ts: '2026-07-23T10:00:00Z',
      kind: 'agent_start',
      fields: { note_id: 9 },
    }
    expect(steerDeliveryModes([other]).size).toBe(0)
  })
})

describe('steerStatusLabel', () => {
  it('names the phase and how the note reached it', () => {
    const n = note({ status: 'delivered', delivered_phase: 'build' })
    expect(steerStatusLabel(n, true)).toBe('delivered in build (mid-phase)')
    expect(steerStatusLabel(n, false)).toBe('delivered in build (at spawn)')
  })

  it('drops the distinction when no delivery event is left to read', () => {
    const n = note({ status: 'delivered', delivered_phase: 'verify' })
    expect(steerStatusLabel(n, undefined)).toBe('delivered in verify')
  })

  it('explains a pending note and one the run settled past', () => {
    expect(steerStatusLabel(note())).toBe('queued — waiting for the next agent')
    expect(steerStatusLabel(note({ status: 'expired' }))).toBe(
      'expired (run ended before delivery)',
    )
  })
})
