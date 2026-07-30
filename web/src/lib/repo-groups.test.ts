import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { isGroupOpen, loadGroupState, saveGroupState } from '@/lib/repo-groups'

const GROUPS_KEY = 'trau.project-groups'

describe('isGroupOpen', () => {
  const members = ['bravo', 'charlie']

  it('opens the group holding the scoped repo and leaves the others closed', () => {
    expect(isGroupOpen({}, 'web', members, 'charlie')).toBe(true)
    expect(isGroupOpen({}, 'web', members, 'alpha')).toBe(false)
  })

  it('closes every group under all repos', () => {
    expect(isGroupOpen({}, 'web', members, null)).toBe(false)
  })

  it('lets a hand-made fold outrank the default either way', () => {
    expect(isGroupOpen({ web: 'closed' }, 'web', members, 'charlie')).toBe(
      false,
    )
    expect(isGroupOpen({ web: 'open' }, 'web', members, 'alpha')).toBe(true)
  })
})

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
    saveGroupState({ web: 'closed', api: 'open' })
    expect(loadGroupState()).toEqual({ web: 'closed', api: 'open' })
  })

  it('falls back to the default rule on anything it cannot use', () => {
    expect(loadGroupState()).toEqual({})
    items.set(GROUPS_KEY, '{oops')
    expect(loadGroupState()).toEqual({})
    items.set(GROUPS_KEY, '["web"]')
    expect(loadGroupState()).toEqual({})
    items.set(GROUPS_KEY, '{"web":"open","api":7}')
    expect(loadGroupState()).toEqual({ web: 'open' })
  })
})
