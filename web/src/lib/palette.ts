import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { ResolvedTheme } from './theme'

// The theme format's web variable set (internal/theme). The hub resolves a
// theme's twelve roles into these; the SPA only applies what it is handed, so
// the derivation rules live in one place.
export const PALETTE_VARS = [
  '--background',
  '--foreground',
  '--card',
  '--card-foreground',
  '--popover',
  '--popover-foreground',
  '--primary',
  '--primary-foreground',
  '--secondary',
  '--secondary-foreground',
  '--muted',
  '--muted-foreground',
  '--accent',
  '--accent-foreground',
  '--destructive',
  '--border',
  '--input',
  '--ring',
  '--brand',
  '--teal',
  '--done',
  '--fail',
  '--warn',
  '--info',
  '--faint',
  '--cli',
] as const

export type PaletteVars = Record<string, string>
export type Palettes = Partial<Record<ResolvedTheme, PaletteVars>>

// ThemeSummary is one theme as the picker lists it. roles carries each defined
// mode's authored colors — before derivation and before any THEME_<ROLE>
// override — so a card's swatch shows the theme, not this repo's resolution.
export interface ThemeSummary {
  slug: string
  name: string
  author?: string
  version?: string
  modes: string[]
  roles?: Record<string, Record<string, string>>
  origin: string
}

export interface ThemesResponse {
  active: string
  themes: ThemeSummary[]
  resolved: Palettes
  // Set when THEME names a theme that is no longer installed — a saved one the
  // picker deleted, say — and the default took over.
  note?: string
}

const COLOR = /^#(?:[0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$/i

// sanitizeVars keeps only known variables holding a color literal. The values
// are injected into a stylesheet, so a cached palette from an older build — or
// anything else that reached localStorage — can never smuggle in CSS.
export function sanitizeVars(input: unknown): PaletteVars | null {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) {
    return null
  }
  const out: PaletteVars = {}
  for (const name of PALETTE_VARS) {
    const value = (input as Record<string, unknown>)[name]
    if (typeof value === 'string' && COLOR.test(value)) out[name] = value
  }
  return Object.keys(out).length > 0 ? out : null
}

export function sanitizePalettes(input: unknown): Palettes {
  if (typeof input !== 'object' || input === null) return {}
  const source = input as Record<string, unknown>
  const out: Palettes = {}
  const light = sanitizeVars(source.light)
  const dark = sanitizeVars(source.dark)
  if (light) out.light = light
  if (dark) out.dark = dark
  return out
}

function paletteBlocks(
  palettes: Palettes,
  lightSelector: string,
  darkSelector: string,
): string {
  const block = (selector: string, vars: PaletteVars | undefined): string => {
    if (!vars) return ''
    const body = PALETTE_VARS.filter((name) => name in vars)
      .map((name) => `${name}:${vars[name]};`)
      .join('')
    return body === '' ? '' : `${selector}{${body}}`
  }
  return (
    block(lightSelector, palettes.light) + block(darkSelector, palettes.dark)
  )
}

// paletteCSS renders the palettes as one stylesheet. Both selectors carry two
// classes' worth of specificity so they outrank the :root/.dark defaults in
// styles.css whatever order the browser sees them in, and light never leaks into
// a dark document.
export function paletteCSS(palettes: Palettes): string {
  return paletteBlocks(palettes, ':root:not(.dark)', ':root.dark')
}

// previewCSS is the same stylesheet one specificity step higher, so an editor
// preview wins over the active theme's palette however the two style elements
// end up ordered in the head.
export function previewCSS(palettes: Palettes): string {
  return paletteBlocks(palettes, ':root:root:not(.dark)', ':root:root.dark')
}

export function paletteBackground(
  palettes: Palettes,
  theme: ResolvedTheme,
): string | null {
  return palettes[theme]?.['--background'] ?? null
}

export async function fetchThemes(repo?: string): Promise<ThemesResponse> {
  const query = repo ? `?repo=${encodeURIComponent(repo)}` : ''
  const res = await apiFetch(`/api/v1/themes${query}`)
  if (!res.ok) throw new Error(`themes: ${res.status}`)
  return (await res.json()) as ThemesResponse
}

export const themesQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['themes', repo],
    queryFn: () => fetchThemes(repo === '' ? undefined : repo),
  })

// themeError surfaces the hub's own message. Theme writes fail by field — a role
// that is not a color, a reserved slug — and that sentence is the whole value of
// the response to an author.
async function themeError(res: Response, fallback: string): Promise<Error> {
  const detail = (await res.json().catch(() => null)) as {
    error?: string
  } | null
  return new Error(detail?.error ?? `${fallback}: ${res.status}`)
}

export interface ThemeSaveResult {
  theme: ThemeSummary
  replaced: boolean
}

// saveTheme installs a theme file under the trau home. A slug already saved
// there is replaced, which the result reports so the editor can say so.
export async function saveTheme(doc: unknown): Promise<ThemeSaveResult> {
  const res = await apiFetch('/api/v1/themes', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(doc),
  })
  if (!res.ok) throw await themeError(res, 'save theme')
  return (await res.json()) as ThemeSaveResult
}

export async function deleteTheme(slug: string): Promise<void> {
  const res = await apiFetch(`/api/v1/themes/${encodeURIComponent(slug)}`, {
    method: 'DELETE',
  })
  if (!res.ok) throw await themeError(res, 'delete theme')
}

// fetchThemeDocument reads a theme as its canonical file — the export, and the
// source 'Duplicate & edit' copies, pinned vars and tui blocks included.
export async function fetchThemeDocument(slug: string): Promise<unknown> {
  const res = await apiFetch(`/api/v1/themes/${encodeURIComponent(slug)}`)
  if (!res.ok) throw await themeError(res, 'read theme')
  return res.json()
}

// resolveDraft expands an unsaved theme into its per-mode variable sets. The
// derivation stays in Go, so the preview is the palette the theme will actually
// paint rather than a browser-side approximation of it.
export async function resolveDraft(doc: unknown): Promise<Palettes> {
  const res = await apiFetch('/api/v1/themes/resolve', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(doc),
  })
  if (!res.ok) throw await themeError(res, 'resolve theme')
  const out = (await res.json()) as { resolved?: unknown }
  return sanitizePalettes(out.resolved)
}
