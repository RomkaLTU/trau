import { isTyping, isUnshiftedKey, type KeyStroke } from './keys'

const NEXT_KEYS = ['ArrowRight', 'ArrowDown']
const PREV_KEYS = ['ArrowLeft', 'ArrowUp']

// A tab list and a radio group wrap where a scrolling list stops: the group is
// small enough to take in whole, so walking off one end and arriving at the
// other reads as one ring rather than as an edge the reader cannot see.
export function wrapIndex(index: number, count: number): number | null {
  if (count <= 0) return null
  return ((index % count) + count) % count
}

// Both axes move, because the same group is laid out in a row on one page and in
// a grid on another, and a reader pressing the arrow that points at the next
// item should not have to know which. A held modifier — Shift included — means
// the keystroke was never the group's, and a field inside the group keeps every
// key the text needs.
export function groupMoveIndex(
  e: KeyStroke,
  current: number,
  count: number,
): number | null {
  if (!isUnshiftedKey(e) || isTyping(e)) return null
  if (NEXT_KEYS.includes(e.key)) return wrapIndex(current + 1, count)
  if (PREV_KEYS.includes(e.key)) return wrapIndex(current - 1, count)
  if (e.key === 'Home') return wrapIndex(0, count)
  if (e.key === 'End') return wrapIndex(count - 1, count)
  return null
}
