import { describe, expect, it } from 'vitest'

import { matchesQuery } from './palette-filter'

describe('matchesQuery', () => {
  it('keeps every row on an empty query', () => {
    expect(matchesQuery('', ['Backlog', '/backlog'])).toBe(true)
    expect(matchesQuery('   ', ['Backlog', '/backlog'])).toBe(true)
  })

  it('matches any term, case-insensitively', () => {
    expect(matchesQuery('back', ['Backlog', '/backlog'])).toBe(true)
    expect(matchesQuery('LOG', ['Backlog', '/backlog'])).toBe(true)
    expect(matchesQuery('/set', ['Settings', '/settings'])).toBe(true)
  })

  it('requires every token, across terms', () => {
    expect(matchesQuery('loop projects', ['loop', '/Users/rd/Projects/loop'])).toBe(
      true,
    )
    expect(matchesQuery('loop tmp', ['loop', '/Users/rd/Projects/loop'])).toBe(false)
  })

  it('drops rows nothing matches', () => {
    expect(matchesQuery('COD-1340', ['Backlog', '/backlog'])).toBe(false)
  })
})
