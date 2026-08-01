export interface PaletteKeyEvent {
  key: string
  metaKey?: boolean
  ctrlKey?: boolean
  shiftKey?: boolean
  isComposing?: boolean
}

// isPaletteShortcut matches the ⌘K / Ctrl+K palette toggle. A bare k is typing,
// and a composing keystroke belongs to the IME.
export function isPaletteShortcut(e: PaletteKeyEvent): boolean {
  if (e.isComposing) return false
  if (!e.metaKey && !e.ctrlKey) return false
  return e.key.toLowerCase() === 'k'
}

const HIGHLIGHT_KEYS = new Set(['ArrowDown', 'ArrowUp', 'Home', 'End'])
const VIM_HIGHLIGHT_KEYS = new Set(['n', 'p', 'j', 'k'])

// movesHighlight matches the keys cmdk binds to move the highlighted row, its
// vim bindings included.
export function movesHighlight(e: PaletteKeyEvent): boolean {
  if (e.isComposing) return false
  if (HIGHLIGHT_KEYS.has(e.key)) return true
  return Boolean(e.ctrlKey) && VIM_HIGHLIGHT_KEYS.has(e.key.toLowerCase())
}

// opensSubmenu matches the Tab that steps from a highlighted result row into
// that ticket's actions. Shift+Tab stays the browser's own backwards focus move.
export function opensSubmenu(e: PaletteKeyEvent): boolean {
  return !e.isComposing && !e.shiftKey && e.key === 'Tab'
}

// leavesSubmenu matches the Backspace that steps back out to the results once
// the submenu's own query has nothing left to erase. Escape steps back too, but
// the dialog would dismiss on it, so the palette claims that key itself.
export function leavesSubmenu(e: PaletteKeyEvent, query: string): boolean {
  return !e.isComposing && e.key === 'Backspace' && query === ''
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
