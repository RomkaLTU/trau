type Store = Pick<Storage, 'getItem' | 'setItem'>

const THEME_KEY = 'trau.theme'

export type ThemeMode = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

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
  return theme
}
