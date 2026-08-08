import { isTyping, isUnshiftedKey, type KeyStroke } from './keys'

// An empty list has no index to hold, so every helper here answers null for it.
export function clampIndex(index: number, count: number): number | null {
  if (count <= 0) return null
  return Math.min(Math.max(index, 0), count - 1)
}

// Movement stops at the ends rather than wrapping: a list that wraps hides where
// it ends from a reader who cannot see the rows go by.
export function nextIndex(current: number, count: number): number | null {
  const at = clampIndex(current, count)
  return at === null ? null : Math.min(at + 1, count - 1)
}

export function prevIndex(current: number, count: number): number | null {
  const at = clampIndex(current, count)
  return at === null ? null : Math.max(at - 1, 0)
}

const NEXT_KEYS = ['ArrowDown', 'j']
const PREV_KEYS = ['ArrowUp', 'k']

// The arrows carry the same guards as the bare j and k: a row that opened an
// editor keeps every key the text needs, and a held modifier — Shift included —
// means the keystroke was never the list's.
export function rowMoveIndex(
  e: KeyStroke,
  current: number,
  count: number,
): number | null {
  if (!isUnshiftedKey(e) || isTyping(e)) return null
  if (NEXT_KEYS.includes(e.key)) return nextIndex(current, count)
  if (PREV_KEYS.includes(e.key)) return prevIndex(current, count)
  if (e.key === 'Home') return clampIndex(0, count)
  if (e.key === 'End') return clampIndex(count - 1, count)
  return null
}

export function activatesRow(e: KeyStroke): boolean {
  return isUnshiftedKey(e) && !isTyping(e) && e.key === 'Enter'
}

// The list claims no letters of its own, so which letters act is the row's business.
export function rowActionKey(e: KeyStroke): string | null {
  if (!isUnshiftedKey(e) || isTyping(e)) return null
  return e.key
}
