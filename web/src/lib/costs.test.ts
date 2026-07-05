import { describe, expect, it } from 'vitest'

import {
  OTHER_KEY,
  collapseSeries,
  compactUsd,
  money,
  priorTotalsByIndex,
  seriesDelta,
  toChartRows,
  type TimeseriesGroup,
  type TimeseriesResponse,
} from '@/lib/costs'

function group(key: string, costs: number[], metered = true): TimeseriesGroup {
  return {
    key,
    tokens: costs.length * 1000,
    cost_usd: costs.reduce((a, b) => a + b, 0),
    metered,
    points: costs.map((cost_usd, i) => ({
      date: `2026-07-0${i + 1}`,
      tokens: 1000,
      cost_usd,
    })),
  }
}

function response(series: TimeseriesGroup[]): TimeseriesResponse {
  return {
    from: '2026-07-01',
    to: '2026-07-02',
    days: series[0]?.points.length ?? 0,
    group_by: 'repo',
    totals: {
      tokens: series.reduce((s, g) => s + g.tokens, 0),
      cost_usd: series.reduce((s, g) => s + g.cost_usd, 0),
      metered: series.every((g) => g.metered),
    },
    series,
    facets: { repos: [], providers: [], models: [], phases: [] },
  }
}

describe('toChartRows', () => {
  it('pivots series into one row per day keyed by series', () => {
    const { rows, keys } = toChartRows([
      group('loop', [1, 2]),
      group('salonradar', [3, 4]),
    ])
    expect(keys).toEqual(['loop', 'salonradar'])
    expect(rows).toEqual([
      { date: '2026-07-01', loop: 1, salonradar: 3 },
      { date: '2026-07-02', loop: 2, salonradar: 4 },
    ])
  })

  it('folds everything past the top 8 into an "other" bucket', () => {
    const series = Array.from({ length: 10 }, (_, i) =>
      group(`r${i}`, [i + 1, 1]),
    )
    const { keys, rows } = toChartRows(series)
    expect(keys).toHaveLength(9)
    expect(keys[8]).toBe(OTHER_KEY)
    expect(rows[0][OTHER_KEY]).toBe(9 + 10)
  })

  it('returns empty rows for no series', () => {
    expect(toChartRows([])).toEqual({ rows: [], keys: [] })
  })
})

describe('collapseSeries', () => {
  it('passes small series through untouched', () => {
    const rows = collapseSeries([group('loop', [4]), group('m4c', [1])])
    expect(rows.map((r) => r.key)).toEqual(['loop', 'm4c'])
    expect(rows[0].cost_usd).toBe(4)
  })

  it('collapses the tail into other and marks it unmetered when any tail row is', () => {
    const series = Array.from({ length: 9 }, (_, i) =>
      group(`r${i}`, [1], i !== 8),
    )
    const rows = collapseSeries(series)
    const other = rows[rows.length - 1]
    expect(other.key).toBe(OTHER_KEY)
    expect(other.metered).toBe(false)
    expect(other.tokens).toBe(1000)
  })
})

describe('priorTotalsByIndex', () => {
  it('sums the prior window series to one figure per day index', () => {
    const prior = response([group('loop', [1, 2]), group('m4c', [3, 4])])
    expect(priorTotalsByIndex(prior, 2)).toEqual([4, 6])
  })
})

describe('seriesDelta', () => {
  it('pairs current and prior spend and computes signed deltas', () => {
    const current = response([group('loop', [6]), group('new', [2])])
    const prior = response([group('loop', [4]), group('gone', [5])])
    const rows = seriesDelta(current, prior)
    const loop = rows.find((r) => r.key === 'loop')!
    expect(loop).toMatchObject({ prev: 4, cur: 6, delta: 2, pct: 50 })
    expect(rows.find((r) => r.key === 'new')!.pct).toBeNull()
    expect(rows.find((r) => r.key === 'gone')!.cur).toBe(0)
  })
})

describe('formatting', () => {
  it('marks unmetered spend as a lower bound', () => {
    expect(money(12.5, true)).toBe('$12.50')
    expect(money(12.5, false)).toBe('≥ $12.50')
  })

  it('compacts thousands on the axis', () => {
    expect(compactUsd(6)).toBe('$6')
    expect(compactUsd(6.5)).toBe('$6.5')
    expect(compactUsd(1200)).toBe('$1.2k')
  })
})
