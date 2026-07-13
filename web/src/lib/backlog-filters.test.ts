import { describe, expect, it } from 'vitest'

import {
  backlogFilterParsers,
  backlogParamsFromFilters,
  hasActiveFilters,
  toggleStateGroup,
  type BacklogFilters,
} from './backlog-filters'

const PAGE_SIZE = 50

function filters(over: Partial<BacklogFilters> = {}): BacklogFilters {
  return { q: '', state: [], label: '', source: null, page: 1, ...over }
}

describe('backlogParamsFromFilters', () => {
  it('maps the default filters to an unfiltered first page', () => {
    expect(backlogParamsFromFilters(filters(), PAGE_SIZE)).toEqual({
      q: '',
      label: '',
      state: '',
      source: '',
      limit: 50,
      offset: 0,
    })
  })

  it('comma-joins the state groups and derives the offset from the 1-based page', () => {
    const params = backlogParamsFromFilters(
      filters({ state: ['started', 'unstarted'], page: 3 }),
      PAGE_SIZE,
    )
    expect(params.state).toBe('started,unstarted')
    expect(params.offset).toBe(100)
    expect(params.limit).toBe(50)
  })

  it('passes q, label and source through', () => {
    expect(
      backlogParamsFromFilters(
        filters({ q: 'auth', label: 'bug', source: 'internal' }),
        PAGE_SIZE,
      ),
    ).toMatchObject({ q: 'auth', label: 'bug', source: 'internal' })
  })
})

describe('hasActiveFilters', () => {
  it('is false for the default view', () => {
    expect(hasActiveFilters(filters())).toBe(false)
  })

  it.each([
    { name: 'search', over: { q: 'x' } },
    { name: 'label', over: { label: 'x' } },
    { name: 'state', over: { state: ['done'] } },
    { name: 'source', over: { source: 'synced' as const } },
  ])('is true when $name is set', ({ over }) => {
    expect(hasActiveFilters(filters(over))).toBe(true)
  })

  it('ignores the page number', () => {
    expect(hasActiveFilters(filters({ page: 4 }))).toBe(false)
  })
})

describe('toggleStateGroup', () => {
  it('adds a group that is not yet selected', () => {
    expect(toggleStateGroup(['started'], 'done')).toEqual(['started', 'done'])
  })

  it('removes a group that is already selected', () => {
    expect(toggleStateGroup(['started', 'done'], 'started')).toEqual(['done'])
  })

  it('returns survivors in STATE_GROUPS order regardless of click order', () => {
    expect(toggleStateGroup(['done'], 'backlog')).toEqual(['backlog', 'done'])
    expect(toggleStateGroup(['canceled', 'backlog'], 'started')).toEqual([
      'backlog',
      'started',
      'canceled',
    ])
  })
})

describe('backlogFilterParsers', () => {
  it('comma-serializes the state array both ways', () => {
    expect(backlogFilterParsers.state.serialize(['a', 'b'])).toBe('a,b')
    expect(backlogFilterParsers.state.parse('a,b')).toEqual(['a', 'b'])
  })

  it('parses page as an integer and rejects unknown sources', () => {
    expect(backlogFilterParsers.page.parse('3')).toBe(3)
    expect(backlogFilterParsers.source.parse('internal')).toBe('internal')
    expect(backlogFilterParsers.source.parse('bogus')).toBeNull()
  })

  it('rejects non-positive and non-integer pages so the hook falls back to page 1', () => {
    expect(backlogFilterParsers.page.parse('0')).toBeNull()
    expect(backlogFilterParsers.page.parse('-1')).toBeNull()
    expect(backlogFilterParsers.page.parse('abc')).toBeNull()
  })
})
