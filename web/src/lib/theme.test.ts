import { describe, expect, it } from 'vitest'

import { resolveTheme } from '@/lib/theme'

describe('resolveTheme', () => {
  it('follows the system preference when the mode is "system"', () => {
    expect(resolveTheme('system', true)).toBe('dark')
    expect(resolveTheme('system', false)).toBe('light')
  })

  it('honors an explicit mode regardless of the system preference', () => {
    expect(resolveTheme('light', true)).toBe('light')
    expect(resolveTheme('light', false)).toBe('light')
    expect(resolveTheme('dark', true)).toBe('dark')
    expect(resolveTheme('dark', false)).toBe('dark')
  })
})
