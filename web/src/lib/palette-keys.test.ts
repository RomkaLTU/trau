import { describe, expect, it } from 'vitest'

import {
  isMacPlatform,
  isPaletteShortcut,
  isRepoPickerShortcut,
  leavesSubmenu,
  movesHighlight,
  opensSubmenu,
  shortcutLabel,
} from './palette-keys'

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

describe('isRepoPickerShortcut', () => {
  it('fires on the modified p', () => {
    expect(isRepoPickerShortcut({ key: 'p', metaKey: true })).toBe(true)
    expect(isRepoPickerShortcut({ key: 'P', ctrlKey: true })).toBe(true)
  })

  it('ignores a bare p — that is typing', () => {
    expect(isRepoPickerShortcut({ key: 'p' })).toBe(false)
  })

  it('leaves the palette chord alone', () => {
    expect(isRepoPickerShortcut({ key: 'k', metaKey: true })).toBe(false)
    expect(isPaletteShortcut({ key: 'p', metaKey: true })).toBe(false)
  })

  it('yields to an IME composing the keystroke', () => {
    expect(
      isRepoPickerShortcut({ key: 'p', metaKey: true, isComposing: true }),
    ).toBe(false)
  })
})

describe('movesHighlight', () => {
  it('matches the arrow and vim keys that walk the list', () => {
    expect(movesHighlight({ key: 'ArrowDown' })).toBe(true)
    expect(movesHighlight({ key: 'ArrowUp' })).toBe(true)
    expect(movesHighlight({ key: 'n', ctrlKey: true })).toBe(true)
  })

  it('ignores plain typing', () => {
    expect(movesHighlight({ key: 'n' })).toBe(false)
    expect(movesHighlight({ key: 'Enter' })).toBe(false)
  })
})

describe('opensSubmenu', () => {
  it('matches the Tab that steps into a row’s actions', () => {
    expect(opensSubmenu({ key: 'Tab' })).toBe(true)
  })

  it('ignores typing and a composing keystroke', () => {
    expect(opensSubmenu({ key: 't' })).toBe(false)
    expect(opensSubmenu({ key: 'Tab', isComposing: true })).toBe(false)
  })

  it('leaves Shift+Tab to move focus backwards', () => {
    expect(opensSubmenu({ key: 'Tab', shiftKey: true })).toBe(false)
  })
})

describe('leavesSubmenu', () => {
  it('steps back out on Backspace once the query is empty', () => {
    expect(leavesSubmenu({ key: 'Backspace' }, '')).toBe(true)
  })

  it('erases the query instead while there is one', () => {
    expect(leavesSubmenu({ key: 'Backspace' }, 'arch')).toBe(false)
  })

  it('ignores other keys', () => {
    expect(leavesSubmenu({ key: 'Escape' }, '')).toBe(false)
    expect(leavesSubmenu({ key: 'a' }, '')).toBe(false)
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

  it('takes the key it labels', () => {
    expect(shortcutLabel(true, 'P')).toBe('⌘P')
    expect(shortcutLabel(false, 'P')).toBe('Ctrl P')
  })
})
