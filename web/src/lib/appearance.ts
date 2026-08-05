import type { ThemeSummary } from './palette'
import type { ResolvedTheme } from './theme'

// SWATCH_ROLES are the core roles a theme card paints its strip from: the two
// base tones, the two accents, then the status colors, in the order they read.
export const SWATCH_ROLES = [
  'surface',
  'text',
  'brand',
  'accent',
  'success',
  'warning',
  'error',
  'info',
] as const

export const MODES: readonly ResolvedTheme[] = ['light', 'dark']

export function definedModes(theme: ThemeSummary): ResolvedTheme[] {
  return MODES.filter((mode) => theme.modes.includes(mode))
}

// previewMode is the mode a card paints itself in: the one the page is showing
// when the theme defines it, otherwise the single mode it does define — a card
// for a dark-only theme still shows its own colors on a light page. A theme that
// defines no mode has nothing to preview.
export function previewMode(
  theme: ThemeSummary,
  mode: ResolvedTheme,
): ResolvedTheme | null {
  const defined = definedModes(theme)
  if (defined.includes(mode)) return mode
  return defined[0] ?? null
}

export function previewRoles(
  theme: ThemeSummary,
  mode: ResolvedTheme,
): Record<string, string> | null {
  const preview = previewMode(theme, mode)
  if (!preview) return null
  return theme.roles?.[preview] ?? null
}

export interface Swatch {
  role: string
  color: string
}

export function swatches(theme: ThemeSummary, mode: ResolvedTheme): Swatch[] {
  const roles = previewRoles(theme, mode)
  if (!roles) return []
  const out: Swatch[] = []
  for (const role of SWATCH_ROLES) {
    const color = roles[role]
    if (color) out.push({ role, color })
  }
  return out
}

// shadowedWriteNote names the layer a pick actually landed in, which is the one
// captured when the write went out rather than whatever the write-target control
// shows now, so a persisted-but-inert pick never claims a layer nothing reached.
export function shadowedWriteNote(
  value: string,
  target: string,
  effectiveLayer: string,
): string {
  return `THEME=${value} was written to the ${target} layer, but the ${effectiveLayer} layer still decides which theme applies.`
}

// modeFallbackNote spells out what a one-mode theme does in the mode it does not
// paint, so the light/dark toggle landing on trau's built-in palette reads as the
// theme's shape rather than as a bug.
export function modeFallbackNote(theme: ThemeSummary): string | null {
  const defined = definedModes(theme)
  if (defined.length !== 1) return null
  const missing = defined[0] === 'dark' ? 'light' : 'dark'
  return `no ${missing} mode — ${missing} keeps trau's built-in palette`
}
