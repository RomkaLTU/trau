import { isBareKey, isTyping, type KeyStroke } from './keys'

// A bare g arms the navigator for this long. The next bare key inside the window
// resolves the jump; anything later starts the sequence over.
export const G_WINDOW_MS = 1000

export type GStep =
  | { kind: 'idle' }
  | { kind: 'arm' }
  | { kind: 'go'; key: string }
  | { kind: 'disarm' }

// gSequenceStep reduces one keystroke against the pending g. armedAt is when the
// g was seen, or null while nothing is pending; targets holds the second keys the
// route map knows. A keystroke that already means something else — modified, typed
// into a field, or under an open layer — disarms silently, as an unknown second
// key does.
export function gSequenceStep(
  e: KeyStroke,
  armedAt: number | null,
  now: number,
  targets: ReadonlySet<string>,
): GStep {
  const armed = armedAt !== null && now - armedAt <= G_WINDOW_MS
  if (!isBareKey(e) || e.layerOpen || isTyping(e)) {
    return armed ? { kind: 'disarm' } : { kind: 'idle' }
  }
  if (!armed) return e.key === 'g' ? { kind: 'arm' } : { kind: 'idle' }
  return targets.has(e.key) ? { kind: 'go', key: e.key } : { kind: 'disarm' }
}

// opensShortcutsHelp matches the ? that shows the shortcuts dialog. It rides
// Shift+/ on most layouts, so the key itself is the only signal.
export function opensShortcutsHelp(e: KeyStroke): boolean {
  if (!isBareKey(e) || e.layerOpen || isTyping(e)) return false
  return e.key === '?'
}
