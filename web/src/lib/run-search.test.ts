import { describe, expect, it } from 'vitest'

import { matchRuns } from './run-search'
import type { Run } from './runs'

function run(overrides: Partial<Run> & { ticket: string }): Run {
  return {
    phase: 'merged',
    phase_rank: 6,
    terminal: true,
    ...overrides,
  }
}

const runs = [
  run({
    ticket: 'COD-1340',
    title: 'Palette v2: bigger modal',
    updated_at: '2026-07-30T10:00:00Z',
  }),
  run({
    ticket: 'COD-1342',
    title: 'Runs search in the palette',
    updated_at: '2026-08-01T09:00:00Z',
  }),
  run({ ticket: 'COD-77', updated_at: '2026-07-01T09:00:00Z' }),
]

describe('matchRuns', () => {
  it('matches a ticket id case-insensitively', () => {
    expect(matchRuns(runs, 'cod-1342').map((r) => r.ticket)).toEqual(['COD-1342'])
  })

  it('matches part of a title', () => {
    expect(matchRuns(runs, 'modal').map((r) => r.ticket)).toEqual(['COD-1340'])
  })

  it('leads with the run touched most recently', () => {
    expect(matchRuns(runs, 'cod').map((r) => r.ticket)).toEqual([
      'COD-1342',
      'COD-1340',
      'COD-77',
    ])
  })

  it('lists nothing for a blank query or a query the ledger has no run for', () => {
    expect(matchRuns(runs, '')).toEqual([])
    expect(matchRuns(runs, '   ')).toEqual([])
    expect(matchRuns(runs, 'COD-9')).toEqual([])
  })

  it('caps the group at five rows', () => {
    const many = Array.from({ length: 9 }, (_, i) =>
      run({ ticket: `COD-${i}`, updated_at: `2026-07-0${i + 1}T09:00:00Z` }),
    )
    expect(matchRuns(many, 'cod')).toHaveLength(5)
  })
})
