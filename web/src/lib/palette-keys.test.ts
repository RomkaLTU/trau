import { describe, expect, it } from 'vitest'

import { isMacPlatform, isPaletteShortcut, shortcutLabel } from './palette-keys'

describe('isPaletteShortcut', () => {
  it('fires on the modified k', () => {
    expect(isPaletteShortcut({ key: 'k', metaKey: true })).toBe(true)
    expect(isPaletteShortcut({ key: 'k', ctrlKey: true })).toBe(true)
  })

  it('ignores a bare k — that is typing', () => {
    expect(isPaletteShortcut({ key: 'k' })).toBe(false)
  })

  it('ignores other modified keys', () => {
    expect(isPaletteShortcut({ key: 'j', metaKey: true })).toBe(false)
    expect(isPaletteShortcut({ key: 'Enter', ctrlKey: true })).toBe(false)
  })

  it('yields to an IME composing the keystroke', () => {
    expect(
      isPaletteShortcut({ key: 'k', metaKey: true, isComposing: true }),
    ).toBe(false)
  })
})

describe('isMacPlatform', () => {
  it('reads userAgentData first', () => {
    expect(
      isMacPlatform({
        userAgentData: { platform: 'macOS' },
        platform: 'Linux x86_64',
      }),
    ).toBe(true)
    expect(
      isMacPlatform({
        userAgentData: { platform: 'Windows' },
        platform: 'MacIntel',
      }),
    ).toBe(false)
  })

  it('falls back to navigator.platform', () => {
    expect(isMacPlatform({ platform: 'MacIntel' })).toBe(true)
    expect(isMacPlatform({ platform: 'Win32' })).toBe(false)
    expect(isMacPlatform({})).toBe(false)
  })
})

describe('shortcutLabel', () => {
  it('shows the platform chord', () => {
    expect(shortcutLabel(true)).toBe('⌘K')
    expect(shortcutLabel(false)).toBe('Ctrl K')
  })
})
