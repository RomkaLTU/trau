import { describe, expect, it } from 'vitest'

import { boardColumns, boardPill } from '@/lib/board'
import type { Run } from '@/lib/runs'

function run(partial: Partial<Run> & { ticket: string; phase: string }): Run {
  return { phase_rank: 0, terminal: false, ...partial }
}

describe('boardPill', () => {
  it('maps pipeline phases through the shared phase pill', () => {
    expect(boardPill({ phase: 'building' })).toEqual({ state: 'active', label: 'build' })
    expect(boardPill({ phase: 'verified' })).toEqual({ state: 'verify', label: 'verify' })
    expect(boardPill({ phase: 'merged' })).toEqual({ state: 'success', label: 'merged' })
  })

  it('lets a failure class win over the phase', () => {
    expect(boardPill({ phase: 'verified', failure_class: 'paused' })).toEqual({
      state: 'warn',
      label: 'paused',
    })
    expect(boardPill({ phase: 'building', failure_class: 'faulted' })).toEqual({
      state: 'fail',
      label: 'fault',
    })
    expect(boardPill({ phase: 'verified', failure_class: 'gave_up' })).toEqual({
      state: 'fail',
      label: 'quarantined',
    })
  })
})

describe('boardColumns', () => {
  it('always draws the pipeline phases in order, even when empty', () => {
    expect(boardColumns([]).map((c) => c.key)).toEqual([
      'queued',
      'build',
      'handoff',
      'verify',
      'pr',
      'merged',
    ])
  })

  it('buckets runs into their phase column', () => {
    const runs = [
      run({ ticket: 'A-1', phase: '' }),
      run({ ticket: 'A-2', phase: 'building' }),
      run({ ticket: 'A-3', phase: 'built' }),
      run({ ticket: 'A-4', phase: 'pr_open' }),
    ]
    const byKey = Object.fromEntries(boardColumns(runs).map((c) => [c.key, c.runs.map((r) => r.ticket)]))
    expect(byKey.queued).toEqual(['A-1'])
    expect(byKey.build).toEqual(['A-2', 'A-3'])
    expect(byKey.pr).toEqual(['A-4'])
    expect(byKey.verify).toEqual([])
  })

  it('orders merged runs most-recent first', () => {
    const runs = [
      run({ ticket: 'A-1', phase: 'merged', updated_at: '2026-07-01T00:00:00Z' }),
      run({ ticket: 'A-2', phase: 'merged', updated_at: '2026-07-05T00:00:00Z' }),
      run({ ticket: 'A-3', phase: 'merged', updated_at: '2026-07-03T00:00:00Z' }),
    ]
    const merged = boardColumns(runs).find((c) => c.key === 'merged')
    expect(merged?.runs.map((r) => r.ticket)).toEqual(['A-2', 'A-3', 'A-1'])
  })

  it('appends off-pipeline phases as trailing columns by rank', () => {
    const runs = [
      run({ ticket: 'A-1', phase: 'quarantined' }),
      run({ ticket: 'A-2', phase: 'building' }),
    ]
    const columns = boardColumns(runs)
    const quarantined = columns[columns.length - 1]
    expect(quarantined.key).toBe('quarantined')
    expect(quarantined.label).toBe('quarantined')
    expect(quarantined.runs.map((r) => r.ticket)).toEqual(['A-1'])
  })
})
