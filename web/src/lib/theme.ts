import { useSyncExternalStore } from 'react'

import { ALL_SCOPE, loadStoredScope } from './active-repo'
import {
  fetchThemes,
  paletteBackground,
  paletteCSS,
  previewCSS,
  sanitizePalettes,
  type Palettes,
} from './palette'

type Store = Pick<Storage, 'getItem' | 'setItem'>

const THEME_KEY = 'trau.theme'
const PALETTE_KEY = 'trau.palette'
const PALETTE_STYLE_ID = 'trau-palette'
const PREVIEW_STYLE_ID = 'trau-palette-preview'

export type ThemeMode = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

// The built-in palette's --background, for the window chrome before a theme's
// own palette has been applied.
const CHROME_COLOR: Record<ResolvedTheme, string> = {
  light: '#faf8f5',
  dark: '#0c0a09',
}

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

function isThemeMode(value: string | null): value is ThemeMode {
  return value === 'system' || value === 'light' || value === 'dark'
}

export function loadThemeMode(): ThemeMode {
  const stored = browserStore()?.getItem(THEME_KEY) ?? null
  return isThemeMode(stored) ? stored : 'system'
}

export function storeThemeMode(mode: ThemeMode): void {
  browserStore()?.setItem(THEME_KEY, mode)
  publish(mode)
}

const listeners = new Set<() => void>()
let snapshot: ThemeMode | null = null

function currentMode(): ThemeMode {
  if (snapshot === null) snapshot = loadThemeMode()
  return snapshot
}

// Passing null drops the snapshot, so the next read comes off storage — which is
// what another tab's write leaves behind.
function publish(mode: ThemeMode | null): void {
  snapshot = mode
  listeners.forEach((notify) => notify())
}

function onStorage(event: StorageEvent): void {
  if (event.key === null || event.key === THEME_KEY) publish(null)
}

function subscribe(notify: () => void): () => void {
  if (listeners.size === 0) globalThis.addEventListener('storage', onStorage)
  listeners.add(notify)
  return () => {
    listeners.delete(notify)
    if (listeners.size === 0) {
      globalThis.removeEventListener('storage', onStorage)
    }
  }
}

// The sidebar toggle and the palette's theme action are two views of one setting,
// so the mode lives here rather than in either of them.
export function useThemeMode(): ThemeMode {
  return useSyncExternalStore(subscribe, currentMode)
}

export function resolveTheme(
  mode: ThemeMode,
  prefersDark: boolean,
): ResolvedTheme {
  if (mode === 'system') return prefersDark ? 'dark' : 'light'
  return mode
}

function systemPrefersDark(): boolean {
  try {
    return (
      globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false
    )
  } catch {
    return false
  }
}

export function applyTheme(mode: ThemeMode): ResolvedTheme {
  const theme = resolveTheme(mode, systemPrefersDark())
  globalThis.document?.documentElement.classList.toggle('dark', theme === 'dark')
  const shown = preview ?? palettes
  globalThis.document
    ?.querySelector('meta[name="theme-color"]')
    ?.setAttribute(
      'content',
      paletteBackground(shown, theme) ?? CHROME_COLOR[theme],
    )
  return theme
}

// The active theme's resolved palettes. They layer over the styles.css defaults
// rather than replacing them, so a mode the theme does not define keeps the
// built-in look instead of inheriting the other mode's colors.
let palettes: Palettes = {}

// The theme editor's draft, applied over the active palette while the editor is
// open. It is browser state and nothing else: nothing is written to the hub, to
// config, or to the palette cache, so a reload — or Cancel — is back on the
// active theme immediately.
let preview: Palettes | null = null

export function previewPalettes(next: Palettes | null): void {
  preview = next
  const doc = globalThis.document
  if (!doc) return
  let style = doc.getElementById(PREVIEW_STYLE_ID)
  if (next === null) {
    style?.remove()
    applyTheme(loadThemeMode())
    return
  }
  if (!style) {
    style = doc.createElement('style')
    style.id = PREVIEW_STYLE_ID
    doc.head.append(style)
  }
  style.textContent = previewCSS(next)
  applyTheme(loadThemeMode())
}

function applyPalettes(next: Palettes): void {
  palettes = next
  const doc = globalThis.document
  if (!doc) return
  let style = doc.getElementById(PALETTE_STYLE_ID)
  if (!style) {
    style = doc.createElement('style')
    style.id = PALETTE_STYLE_ID
    doc.head.append(style)
  }
  style.textContent = paletteCSS(next)
  applyTheme(loadThemeMode())
}

function loadCachedPalettes(): Palettes {
  const raw = browserStore()?.getItem(PALETTE_KEY) ?? null
  if (!raw) return {}
  try {
    return sanitizePalettes(JSON.parse(raw))
  } catch {
    return {}
  }
}

// refreshPalettes re-reads the resolved palettes from the hub and repaints. repo
// names whose THEME applies; omitting it falls back to the stored project scope,
// which is what a reload resolves. Callers use it after writing THEME so the new
// theme lands without one.
export async function refreshPalettes(repo?: string): Promise<void> {
  const scope = repo ?? loadStoredScope()
  try {
    const res = await fetchThemes(
      scope && scope !== ALL_SCOPE ? scope : undefined,
    )
    const resolved = sanitizePalettes(res.resolved)
    applyPalettes(resolved)
    browserStore()?.setItem(PALETTE_KEY, JSON.stringify(resolved))
  } catch {
    // A hub that cannot answer leaves the cached or built-in palette in place;
    // the connectivity surfaces already report an unreachable hub.
  }
}

// initPalettes paints the last known palette first — the cache is also what the
// pre-paint script in index.html reads — then reconciles with the hub. The
// project scope decides whose THEME applies, so a repo that sets its own theme
// wins while it is the selected project.
export async function initPalettes(): Promise<void> {
  const cached = loadCachedPalettes()
  if (cached.light || cached.dark) applyPalettes(cached)
  await refreshPalettes()
}
