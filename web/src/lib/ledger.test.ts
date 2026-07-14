import { describe, expect, it } from 'vitest'

import {
  attentionReason,
  bucketCounts,
  bucketOf,
  capMerged,
  formatAge,
  joinInstances,
  rowsForTab,
  sortRows,
  type LedgerRow,
} from '@/lib/ledger'
import type { Instance } from '@/lib/instances'
import type { Run } from '@/lib/runs'

function run(partial: Partial<Run> & { ticket: string }): Run {
  return { phase: '', phase_rank: 0, terminal: false, ...partial }
}

function instance(partial: Partial<Instance>): Instance {
  return {
    pid: 1,
    repo: 'repo',
    repo_root: '/repo',
    runs_dir: '/repo/runs',
    started_at: '2026-07-14T00:00:00Z',
    session_state: 'working',
    ...partial,
  }
}

function row(partial: Partial<Run> & { ticket: string }, live = false): LedgerRow {
  return {
    run: run(partial),
    instance: live ? instance({ ticket: partial.ticket }) : undefined,
  }
}

describe('joinInstances', () => {
  it('pairs a run with the working instance holding its ticket', () => {
    const rows = joinInstances(
      [run({ ticket: 'A-1' }), run({ ticket: 'A-2' })],
      [instance({ ticket: 'A-1', session_state: 'working' })],
      'repo',
    )
    expect(rows[0].instance?.ticket).toBe('A-1')
    expect(rows[1].instance).toBeUndefined()
  })

  it('ignores a non-working, wrong-repo, or ticketless instance', () => {
    const rows = joinInstances(
      [run({ ticket: 'A-1' })],
      [
        instance({ ticket: 'A-1', session_state: 'grazing' }),
        instance({ ticket: 'A-1', repo: 'other' }),
        instance({ ticket: undefined }),
      ],
      'repo',
    )
    expect(rows[0].instance).toBeUndefined()
  })
})

describe('bucketOf', () => {
  it('assigns each run to exactly one bucket by precedence', () => {
    expect(bucketOf(row({ ticket: 'A', phase: 'building' }, true))).toBe('active')
    expect(bucketOf(row({ ticket: 'A', phase: 'built', failure_class: 'faulted' }))).toBe('needs-you')
    expect(bucketOf(row({ ticket: 'A', phase: 'merged' }))).toBe('merged')
    expect(bucketOf(row({ ticket: 'A', phase: 'pr_open' }))).toBe('stopped')
    expect(bucketOf(row({ ticket: 'A', phase: '' }))).toBe('stopped')
  })

  it('lets a live loop win over a stale failure class', () => {
    expect(
      bucketOf(row({ ticket: 'A', phase: 'built', failure_class: 'faulted' }, true)),
    ).toBe('active')
  })

  it('keeps the buckets disjoint — counts sum to the total', () => {
    const rows = [
      row({ ticket: 'A', phase: 'building' }, true),
      row({ ticket: 'B', phase: 'built', failure_class: 'paused' }),
      row({ ticket: 'C', phase: 'merged' }),
      row({ ticket: 'D', phase: 'pr_open' }),
      row({ ticket: 'E', phase: '' }),
    ]
    const counts = bucketCounts(rows)
    expect(counts.active + counts['needs-you'] + counts.merged + counts.stopped).toBe(rows.length)
    expect(counts).toEqual({ active: 1, 'needs-you': 1, merged: 1, stopped: 2 })
  })
})

describe('sortRows', () => {
  it('orders needs-you → active → stopped → merged', () => {
    const rows = [
      row({ ticket: 'M', phase: 'merged', updated_at: '2026-07-10T00:00:00Z' }),
      row({ ticket: 'S', phase: 'pr_open', updated_at: '2026-07-11T00:00:00Z' }),
      row({ ticket: 'N', phase: 'built', failure_class: 'faulted', updated_at: '2026-07-09T00:00:00Z' }),
      row({ ticket: 'A', phase: 'building', updated_at: '2026-07-08T00:00:00Z' }, true),
    ]
    expect(sortRows(rows).map((r) => r.run.ticket)).toEqual(['N', 'A', 'S', 'M'])
  })

  it('sorts newest first within a bucket', () => {
    const rows = [
      row({ ticket: 'M1', phase: 'merged', updated_at: '2026-07-01T00:00:00Z' }),
      row({ ticket: 'M2', phase: 'merged', updated_at: '2026-07-05T00:00:00Z' }),
      row({ ticket: 'M3', phase: 'merged', updated_at: '2026-07-03T00:00:00Z' }),
    ]
    expect(sortRows(rows).map((r) => r.run.ticket)).toEqual(['M2', 'M3', 'M1'])
  })
})

describe('rowsForTab', () => {
  const rows = [
    row({ ticket: 'A', phase: 'building' }, true),
    row({ ticket: 'N', phase: 'built', failure_class: 'faulted' }),
    row({ ticket: 'M', phase: 'merged' }),
    row({ ticket: 'S', phase: 'pr_open' }),
  ]

  it('keeps the whole ledger for the all tab', () => {
    expect(rowsForTab(rows, 'all')).toHaveLength(rows.length)
  })

  it('narrows to a single bucket for a bucket tab', () => {
    expect(rowsForTab(rows, 'merged').map((r) => r.run.ticket)).toEqual(['M'])
    expect(rowsForTab(rows, 'needs-you').map((r) => r.run.ticket)).toEqual(['N'])
    expect(rowsForTab(rows, 'active').map((r) => r.run.ticket)).toEqual(['A'])
  })
})

describe('capMerged', () => {
  function merged(n: number): LedgerRow[] {
    return Array.from({ length: n }, (_, i) =>
      row({ ticket: `M-${i}`, phase: 'merged', updated_at: `2026-07-${String(i + 1).padStart(2, '0')}T00:00:00Z` }),
    )
  }

  it('caps the merged tail at MERGED_CAP and reports the remainder', () => {
    const capped = capMerged(sortRows(merged(20)), false)
    expect(capped.rows).toHaveLength(15)
    expect(capped.hidden).toBe(5)
  })

  it('shows every merged row when expanded', () => {
    const sorted = sortRows(merged(20))
    expect(capMerged(sorted, true)).toEqual({ rows: sorted, hidden: 0 })
  })

  it('never hides a non-merged row', () => {
    const rows = sortRows([
      ...merged(20),
      row({ ticket: 'S1', phase: 'pr_open', updated_at: '2026-07-12T00:00:00Z' }),
      row({ ticket: 'N1', phase: 'built', failure_class: 'faulted', updated_at: '2026-07-12T00:00:00Z' }),
    ])
    const capped = capMerged(rows, false)
    expect(capped.rows.filter((r) => bucketOf(r) !== 'merged')).toHaveLength(2)
    expect(capped.rows.filter((r) => bucketOf(r) === 'merged')).toHaveLength(15)
    expect(capped.hidden).toBe(5)
  })

  it('does not cap when merged rows fit', () => {
    expect(capMerged(sortRows(merged(3)), false).hidden).toBe(0)
  })
})

describe('attentionReason', () => {
  it('prefers the loop’s own failure reason', () => {
    expect(
      attentionReason(
        run({ ticket: 'A', failure_class: 'faulted', failure_reason: 'build timeout after 3 retries' }),
      ),
    ).toBe('build timeout after 3 retries')
  })

  it('falls back to the failure class when no reason was written', () => {
    expect(attentionReason(run({ ticket: 'A', failure_class: 'gave_up' }))).toBe('quarantined')
    expect(attentionReason(run({ ticket: 'A', failure_class: 'paused', failure_reason: '  ' }))).toBe('paused')
  })
})

describe('formatAge', () => {
  it('renders compact relative spans across the boundaries', () => {
    expect(formatAge(30_000)).toBe('just now')
    expect(formatAge(5 * 60_000)).toBe('5m')
    expect(formatAge((2 * 3600 + 11 * 60) * 1000)).toBe('2h 11m')
    expect(formatAge(25 * 3600 * 1000)).toBe('1d')
  })
})
