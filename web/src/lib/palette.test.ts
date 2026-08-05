import { describe, expect, it } from 'vitest'

import {
  paletteBackground,
  paletteCSS,
  sanitizePalettes,
  sanitizeVars,
} from '@/lib/palette'

describe('sanitizeVars', () => {
  it('keeps known variables holding a color literal', () => {
    expect(
      sanitizeVars({ '--brand': '#ff7a18', '--background': '#0C0A09' }),
    ).toEqual({ '--brand': '#ff7a18', '--background': '#0C0A09' })
  })

  it('drops unknown names and anything that is not a color', () => {
    expect(
      sanitizeVars({
        '--brand': '#ff7a18',
        '--sparkle': '#ff0000',
        '--ring': 'red; } body { display: none',
        '--input': 'var(--brand)',
      }),
    ).toEqual({ '--brand': '#ff7a18' })
  })

  it('rejects a palette that carries nothing usable', () => {
    expect(sanitizeVars({ '--brand': 'chartreuse' })).toBeNull()
    expect(sanitizeVars(null)).toBeNull()
    expect(sanitizeVars(['#ff7a18'])).toBeNull()
  })
})

describe('sanitizePalettes', () => {
  it('keeps only the modes the theme defines', () => {
    expect(sanitizePalettes({ dark: { '--brand': '#ff0000' } })).toEqual({
      dark: { '--brand': '#ff0000' },
    })
    expect(sanitizePalettes({ sepia: { '--brand': '#ff0000' } })).toEqual({})
    expect(sanitizePalettes('nord')).toEqual({})
  })
})

describe('paletteCSS', () => {
  it('scopes each mode so it cannot leak into the other', () => {
    const css = paletteCSS({
      light: { '--background': '#ffffff' },
      dark: { '--background': '#000000' },
    })
    expect(css).toBe(
      ':root:not(.dark){--background:#ffffff;}:root.dark{--background:#000000;}',
    )
  })

  it('emits nothing for a mode the theme does not define', () => {
    expect(paletteCSS({ dark: { '--brand': '#ff0000' } })).toBe(
      ':root.dark{--brand:#ff0000;}',
    )
    expect(paletteCSS({})).toBe('')
  })
})

describe('paletteBackground', () => {
  it('reads the active mode, and reports nothing when it is undefined', () => {
    const palettes = { dark: { '--background': '#2e3440' } }
    expect(paletteBackground(palettes, 'dark')).toBe('#2e3440')
    expect(paletteBackground(palettes, 'light')).toBeNull()
  })
})
