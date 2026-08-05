import { describe, expect, it } from 'vitest'

import {
  SWATCH_ROLES,
  definedModes,
  modeFallbackNote,
  previewMode,
  previewRoles,
  shadowedWriteNote,
  swatches,
} from '@/lib/appearance'
import type { ThemeSummary } from '@/lib/palette'

const ROLES = [
  'brand',
  'accent',
  'success',
  'error',
  'warning',
  'info',
  'text',
  'subtle',
  'faint',
  'surface',
  'border',
  'ink',
]

function roles(prefix: string): Record<string, string> {
  return Object.fromEntries(
    ROLES.map((role, i) => [role, `#${prefix}${i.toString(16)}0`]),
  )
}

function theme(overrides: Partial<ThemeSummary> = {}): ThemeSummary {
  return {
    slug: 'nord',
    name: 'Nord',
    modes: ['light', 'dark'],
    roles: { light: roles('aa'), dark: roles('11') },
    origin: 'bundled',
    ...overrides,
  }
}

describe('definedModes', () => {
  it('reports the modes a theme paints in light-then-dark order', () => {
    expect(definedModes(theme({ modes: ['dark', 'light'] }))).toEqual([
      'light',
      'dark',
    ])
    expect(definedModes(theme({ modes: ['dark'] }))).toEqual(['dark'])
  })

  it('ignores a mode name the format does not define', () => {
    expect(definedModes(theme({ modes: ['dark', 'sepia'] }))).toEqual(['dark'])
  })
})

describe('previewMode', () => {
  it('paints the card in the mode on screen when the theme defines it', () => {
    expect(previewMode(theme(), 'dark')).toBe('dark')
    expect(previewMode(theme(), 'light')).toBe('light')
  })

  it('falls back to the one mode a single-mode theme does define', () => {
    expect(previewMode(theme({ modes: ['dark'] }), 'light')).toBe('dark')
    expect(previewMode(theme({ modes: ['light'] }), 'dark')).toBe('light')
  })

  it('has nothing to preview when no known mode is defined', () => {
    expect(previewMode(theme({ modes: [] }), 'dark')).toBeNull()
  })
})

describe('swatches', () => {
  it('reads the core roles of the previewed mode in strip order', () => {
    const dark = roles('11')
    expect(swatches(theme(), 'dark')).toEqual(
      SWATCH_ROLES.map((role) => ({ role, color: dark[role] })),
    )
  })

  it('reads the fallback mode for a theme that does not paint this one', () => {
    const only = theme({ modes: ['dark'], roles: { dark: roles('11') } })
    expect(swatches(only, 'light')).toEqual(swatches(only, 'dark'))
  })

  it('drops roles a theme did not send rather than emitting holes', () => {
    const partial = theme({
      roles: { light: { brand: '#ff7a18' }, dark: roles('11') },
    })
    expect(swatches(partial, 'light')).toEqual([
      { role: 'brand', color: '#ff7a18' },
    ])
  })

  it('is empty when the payload carries no roles at all', () => {
    expect(swatches(theme({ roles: undefined }), 'dark')).toEqual([])
    expect(previewRoles(theme({ roles: {} }), 'dark')).toBeNull()
  })
})

describe('shadowedWriteNote', () => {
  it('names the layer the write landed in and the one that still wins', () => {
    expect(shadowedWriteNote('nord', 'project', 'env var')).toBe(
      'THEME=nord was written to the project layer, but the env var layer still decides which theme applies.',
    )
  })
})

describe('modeFallbackNote', () => {
  it('names the mode a one-mode theme leaves to the built-in palette', () => {
    expect(modeFallbackNote(theme({ modes: ['dark'] }))).toContain('no light')
    expect(modeFallbackNote(theme({ modes: ['light'] }))).toContain('no dark')
  })

  it('says nothing for a theme that paints both modes', () => {
    expect(modeFallbackNote(theme())).toBeNull()
  })
})
