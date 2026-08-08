import { describe, expect, it } from 'vitest'

import { G_WINDOW_MS, gSequenceStep, opensShortcutsHelp } from './shortcut-keys'

const TARGETS = new Set(['b', 'l', 'o'])

function step(
  key: string,
  armedAt: number | null,
  now = 0,
  extra: Record<string, unknown> = {},
) {
  return gSequenceStep({ key, ...extra }, armedAt, now, TARGETS)
}

describe('gSequenceStep', () => {
  it('arms on a bare g', () => {
    expect(step('g', null)).toEqual({ kind: 'arm' })
  })

  it('resolves the second key against the route map', () => {
    expect(step('b', 0, 200)).toEqual({ kind: 'go', key: 'b' })
    expect(step('l', 0, 200)).toEqual({ kind: 'go', key: 'l' })
  })

  it('disarms silently on an unknown second key', () => {
    expect(step('z', 0, 200)).toEqual({ kind: 'disarm' })
    expect(step('Escape', 0, 200)).toEqual({ kind: 'disarm' })
    expect(step('g', 0, 200)).toEqual({ kind: 'disarm' })
  })

  it('disarms once the window has passed, and starts over on a fresh g', () => {
    expect(step('b', 0, G_WINDOW_MS + 1)).toEqual({ kind: 'idle' })
    expect(step('g', 0, G_WINDOW_MS + 1)).toEqual({ kind: 'arm' })
  })

  it('still resolves on the last millisecond of the window', () => {
    expect(step('b', 0, G_WINDOW_MS)).toEqual({ kind: 'go', key: 'b' })
  })

  it('does nothing on an unrelated key while nothing is pending', () => {
    expect(step('b', null)).toEqual({ kind: 'idle' })
    expect(step('j', null)).toEqual({ kind: 'idle' })
  })

  it('yields a modified keystroke to the browser', () => {
    expect(step('g', null, 0, { metaKey: true })).toEqual({ kind: 'idle' })
    expect(step('g', null, 0, { ctrlKey: true })).toEqual({ kind: 'idle' })
    expect(step('g', null, 0, { altKey: true })).toEqual({ kind: 'idle' })
    expect(step('b', 0, 200, { metaKey: true })).toEqual({ kind: 'disarm' })
  })

  it('yields to an IME composing the keystroke', () => {
    expect(step('g', null, 0, { isComposing: true })).toEqual({ kind: 'idle' })
  })

  it('yields while focus is in a field or a contenteditable editor', () => {
    expect(step('g', null, 0, { targetTag: 'INPUT' })).toEqual({ kind: 'idle' })
    expect(step('g', null, 0, { targetTag: 'TEXTAREA' })).toEqual({
      kind: 'idle',
    })
    expect(step('g', null, 0, { targetTag: 'SELECT' })).toEqual({ kind: 'idle' })
    expect(
      step('g', null, 0, { targetTag: 'DIV', targetEditable: true }),
    ).toEqual({ kind: 'idle' })
    expect(step('b', 0, 200, { targetTag: 'INPUT' })).toEqual({
      kind: 'disarm',
    })
  })

  it('yields while a layer is open over the workspace', () => {
    expect(step('g', null, 0, { layerOpen: true })).toEqual({ kind: 'idle' })
    expect(step('b', 0, 200, { layerOpen: true })).toEqual({ kind: 'disarm' })
  })

  it('still fires over the page body', () => {
    expect(step('g', null, 0, { targetTag: 'BODY' })).toEqual({ kind: 'arm' })
  })
})

describe('opensShortcutsHelp', () => {
  it('matches the bare ?', () => {
    expect(opensShortcutsHelp({ key: '?' })).toBe(true)
    expect(opensShortcutsHelp({ key: '?', targetTag: 'BODY' })).toBe(true)
  })

  it('leaves the unshifted key alone', () => {
    expect(opensShortcutsHelp({ key: '/' })).toBe(false)
  })

  it('yields a modified keystroke to the browser', () => {
    expect(opensShortcutsHelp({ key: '?', metaKey: true })).toBe(false)
    expect(opensShortcutsHelp({ key: '?', ctrlKey: true })).toBe(false)
    expect(opensShortcutsHelp({ key: '?', altKey: true })).toBe(false)
  })

  it('yields to an IME composing the keystroke', () => {
    expect(opensShortcutsHelp({ key: '?', isComposing: true })).toBe(false)
  })

  it('yields while typing and under an open layer', () => {
    expect(opensShortcutsHelp({ key: '?', targetTag: 'INPUT' })).toBe(false)
    expect(
      opensShortcutsHelp({ key: '?', targetTag: 'DIV', targetEditable: true }),
    ).toBe(false)
    expect(opensShortcutsHelp({ key: '?', layerOpen: true })).toBe(false)
  })
})
