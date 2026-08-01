import { useSyncExternalStore } from 'react'

type Store = Pick<Storage, 'getItem' | 'setItem'>

const THEME_KEY = 'trau.theme'

export type ThemeMode = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

// Mirrors --background so the installed window chrome tracks the app theme.
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
  globalThis.document
    ?.querySelector('meta[name="theme-color"]')
    ?.setAttribute('content', CHROME_COLOR[theme])
  return theme
}
