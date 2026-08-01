import { afterEach, describe, expect, it, vi } from 'vitest'

import { loadActivityShown, storeActivityShown } from './grill-activity'

function stubStorage(seed: Record<string, string> = {}): Map<string, string> {
  const store = new Map(Object.entries(seed))
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => void store.set(key, value),
  })
  return store
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('activity preference', () => {
  it('is off until someone asks for it', () => {
    stubStorage()

    expect(loadActivityShown()).toBe(false)
  })

  it('survives the reload that reads it back', () => {
    const store = stubStorage()
    storeActivityShown(true)

    expect(loadActivityShown()).toBe(true)
    expect(store.get('trau.grill.activity')).toBe('1')
  })

  it('turning it off is remembered as such, not as never asked', () => {
    stubStorage({ 'trau.grill.activity': '1' })
    storeActivityShown(false)

    expect(loadActivityShown()).toBe(false)
  })
})
