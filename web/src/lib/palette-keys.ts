export interface PaletteKeyEvent {
  key: string
  metaKey?: boolean
  ctrlKey?: boolean
  isComposing?: boolean
}

// isPaletteShortcut matches the ⌘K / Ctrl+K palette toggle. A bare k is typing,
// and a composing keystroke belongs to the IME.
export function isPaletteShortcut(e: PaletteKeyEvent): boolean {
  if (e.isComposing) return false
  if (!e.metaKey && !e.ctrlKey) return false
  return e.key.toLowerCase() === 'k'
}

export interface PlatformSource {
  platform?: string
  userAgentData?: { platform?: string }
}

export function isMacPlatform(nav: PlatformSource): boolean {
  return /mac/i.test(nav.userAgentData?.platform ?? nav.platform ?? '')
}

export function shortcutLabel(mac: boolean): string {
  return mac ? '⌘K' : 'Ctrl K'
}
