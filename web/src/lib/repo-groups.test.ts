import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { isGroupOpen, loadGroupState, saveGroupState } from '@/lib/repo-groups'

const GROUPS_KEY = 'trau.project-groups'

describe('loadGroupState', () => {
  let items: Map<string, string>

  beforeEach(() => {
    items = new Map()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => items.get(key) ?? null,
      setItem: (key: string, value: string) => void items.set(key, value),
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('reads back the folds it stored', () => {
    saveGroupState({ platform: 'closed', web: 'open' })
    expect(loadGroupState()).toEqual({ platform: 'closed', web: 'open' })
  })

  it('falls back to no folds on a value that is not a fold map', () => {
    for (const raw of ['{not json', '"open"', '["platform"]']) {
      items.set(GROUPS_KEY, raw)
      expect(loadGroupState()).toEqual({})
    }
  })

  it('keeps the entries carrying a fold and drops the rest', () => {
    items.set(GROUPS_KEY, JSON.stringify({ web: 'open', api: 'maybe' }))
    expect(loadGroupState()).toEqual({ web: 'open' })
  })
})

describe('isGroupOpen', () => {
  const members = ['alpha', 'bravo']

  it('opens the group holding the scoped repo, folding the others', () => {
    expect(isGroupOpen({}, 'platform', members, 'bravo')).toBe(true)
    expect(isGroupOpen({}, 'platform', members, 'charlie')).toBe(false)
  })

  it('folds every group under "All repos"', () => {
    expect(isGroupOpen({}, 'platform', members, null)).toBe(false)
  })

  it('lets an explicit fold outrank the scoped repo', () => {
    expect(isGroupOpen({ platform: 'closed' }, 'platform', members, 'bravo')).toBe(
      false,
    )
    expect(isGroupOpen({ platform: 'open' }, 'platform', members, null)).toBe(true)
  })
})
