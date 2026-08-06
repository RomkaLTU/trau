import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  ACTIVITY_DETAIL_BUDGET,
  loadActivityOpen,
  loadActivityShown,
  storeActivityOpen,
  storeActivityShown,
  truncateActivityDetail,
} from './grill-activity'

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
  it('is on until someone switches it off', () => {
    stubStorage()

    expect(loadActivityShown()).toBe(true)
  })

  it('survives the reload that reads it back', () => {
    const store = stubStorage()
    storeActivityShown(true)

    expect(loadActivityShown()).toBe(true)
    expect(store.get('trau.grill.activity')).toBe('1')
  })

  it('turning it off is remembered as such, not as never asked', () => {
    const store = stubStorage({ 'trau.grill.activity': '1' })
    storeActivityShown(false)

    expect(loadActivityShown()).toBe(false)
    expect(store.get('trau.grill.activity')).toBe('0')
  })
})

describe('feed open preference', () => {
  it('stands open until someone collapses it', () => {
    stubStorage()

    expect(loadActivityOpen()).toBe(true)
  })

  it('survives the remount that reads it back', () => {
    const store = stubStorage()
    storeActivityOpen(false)

    expect(loadActivityOpen()).toBe(false)
    expect(store.get('trau.grill.activity.open')).toBe('0')
  })

  it('is remembered apart from the activity switch', () => {
    const store = stubStorage()
    storeActivityOpen(false)
    storeActivityShown(true)

    expect(loadActivityOpen()).toBe(false)
    expect(loadActivityShown()).toBe(true)
    expect(store.get('trau.grill.activity')).toBe('1')
  })

  it('reopening it is remembered as such', () => {
    const store = stubStorage({ 'trau.grill.activity.open': '0' })
    storeActivityOpen(true)

    expect(loadActivityOpen()).toBe(true)
    expect(store.get('trau.grill.activity.open')).toBe('1')
  })
})

describe('detail truncation', () => {
  it('leaves a detail the row holds alone', () => {
    const detail = 'go test ./internal/webserver/'

    expect(truncateActivityDetail(detail)).toBe(detail)
    expect(truncateActivityDetail('x'.repeat(ACTIVITY_DETAIL_BUDGET))).toHaveLength(
      ACTIVITY_DETAIL_BUDGET,
    )
  })

  it('cuts a long command in the middle, keeping both ends', () => {
    const detail = `git log --oneline ${'a'.repeat(200)} | head -20`
    const got = truncateActivityDetail(detail)

    expect([...got]).toHaveLength(ACTIVITY_DETAIL_BUDGET)
    expect(got.startsWith('git log --oneline')).toBe(true)
    expect(got.endsWith('| head -20')).toBe(true)
    expect(got).toContain('…')
  })

  it('keeps a path detail readable as a path, filename intact', () => {
    const got = truncateActivityDetail(
      `cat ${'nested/'.repeat(20)}deeply-buried-config.json`,
    )

    expect(got.endsWith('/deeply-buried-config.json')).toBe(true)
    expect([...got]).toHaveLength(ACTIVITY_DETAIL_BUDGET)
  })

  it('falls back to an even split when the last segment would eat the row', () => {
    const got = truncateActivityDetail(`run /tmp/${'f'.repeat(120)}.log`)

    expect([...got]).toHaveLength(ACTIVITY_DETAIL_BUDGET)
    expect(got.startsWith('run /tmp/')).toBe(true)
    expect(got.endsWith('.log')).toBe(true)
  })

  it('counts characters rather than code units', () => {
    const got = truncateActivityDetail('é'.repeat(ACTIVITY_DETAIL_BUDGET + 40))

    expect([...got]).toHaveLength(ACTIVITY_DETAIL_BUDGET)
    expect(got).toBe(`${'é'.repeat(40)}…${'é'.repeat(39)}`)
  })

  it('honours a smaller budget, down to head-less', () => {
    expect(truncateActivityDetail('abcdefghij', 5)).toBe('ab…ij')
    expect(truncateActivityDetail('abcdefghij', 2)).toBe('…j')
  })
})
