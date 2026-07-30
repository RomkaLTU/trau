import { afterEach, describe, expect, it, vi } from 'vitest'

import { isDismissed, loadDismissals, markDismissed } from './sync-dismissed'

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

describe('markDismissed', () => {
  it('remembers the error a repo was dismissed for', () => {
    const marks = markDismissed({}, 'loop', 'linear: rate limit exceeded')

    expect(isDismissed(marks, 'loop', 'linear: rate limit exceeded')).toBe(true)
  })

  it('raises the notice again once the repo fails for a new reason', () => {
    const marks = markDismissed({}, 'loop', 'linear: rate limit exceeded')

    expect(isDismissed(marks, 'loop', 'linear: unauthorized')).toBe(false)
  })

  it('keeps the other repos it already holds', () => {
    const marks = markDismissed(
      markDismissed({}, 'loop', 'boom'),
      'melga',
      'thud',
    )

    expect(isDismissed(marks, 'loop', 'boom')).toBe(true)
    expect(isDismissed(marks, 'melga', 'thud')).toBe(true)
  })

  it('leaves a repo nothing was dismissed for alone', () => {
    expect(isDismissed({}, 'loop', 'boom')).toBe(false)
  })
})

describe('loadDismissals', () => {
  it('reads back what was stored', () => {
    stubStorage({
      'trau.sync-degraded.dismissed': JSON.stringify({ loop: 'boom' }),
    })

    expect(isDismissed(loadDismissals(), 'loop', 'boom')).toBe(true)
  })

  it('starts empty on unreadable storage rather than hiding a live failure', () => {
    stubStorage({ 'trau.sync-degraded.dismissed': 'not json' })

    expect(loadDismissals()).toEqual({})
  })
})
