import { describe, expect, it } from 'vitest'

import { type KeyStroke } from './keys'
import {
  activatesRow,
  clampIndex,
  nextIndex,
  prevIndex,
  rowActionKey,
  rowMoveIndex,
} from './roving-list'

function stroke(key: string, over: Partial<KeyStroke> = {}): KeyStroke {
  return { key, ...over }
}

describe('clampIndex', () => {
  it('holds an index inside the list', () => {
    expect(clampIndex(3, 10)).toBe(3)
    expect(clampIndex(-4, 10)).toBe(0)
    expect(clampIndex(99, 10)).toBe(9)
  })

  it('answers null for a list with no rows', () => {
    expect(clampIndex(0, 0)).toBeNull()
    expect(clampIndex(2, -1)).toBeNull()
  })
})

describe('nextIndex and prevIndex', () => {
  it('step one row and stop at both ends', () => {
    expect(nextIndex(0, 3)).toBe(1)
    expect(nextIndex(2, 3)).toBe(2)
    expect(prevIndex(2, 3)).toBe(1)
    expect(prevIndex(0, 3)).toBe(0)
  })

  it('steps from a clamped start', () => {
    expect(nextIndex(-5, 3)).toBe(1)
    expect(prevIndex(9, 3)).toBe(1)
  })

  it('answers null for a list with no rows', () => {
    expect(nextIndex(0, 0)).toBeNull()
    expect(prevIndex(0, 0)).toBeNull()
  })
})

describe('rowMoveIndex', () => {
  it('moves on the arrows and on a bare j/k alike', () => {
    expect(rowMoveIndex(stroke('ArrowDown'), 0, 4)).toBe(1)
    expect(rowMoveIndex(stroke('j'), 0, 4)).toBe(1)
    expect(rowMoveIndex(stroke('ArrowUp'), 2, 4)).toBe(1)
    expect(rowMoveIndex(stroke('k'), 2, 4)).toBe(1)
  })

  it('jumps to the ends on Home and End', () => {
    expect(rowMoveIndex(stroke('Home'), 2, 4)).toBe(0)
    expect(rowMoveIndex(stroke('End'), 2, 4)).toBe(3)
  })

  it('clamps rather than wraps', () => {
    expect(rowMoveIndex(stroke('ArrowUp'), 0, 4)).toBe(0)
    expect(rowMoveIndex(stroke('ArrowDown'), 3, 4)).toBe(3)
  })

  it('leaves a modified or composing stroke alone, a held Shift included', () => {
    expect(rowMoveIndex(stroke('j', { metaKey: true }), 0, 4)).toBeNull()
    expect(rowMoveIndex(stroke('ArrowDown', { ctrlKey: true }), 0, 4)).toBeNull()
    expect(rowMoveIndex(stroke('j', { isComposing: true }), 0, 4)).toBeNull()
    expect(rowMoveIndex(stroke('j', { shiftKey: true }), 0, 4)).toBeNull()
    expect(rowMoveIndex(stroke('ArrowDown', { shiftKey: true }), 0, 4)).toBeNull()
    expect(rowMoveIndex(stroke('End', { shiftKey: true }), 0, 4)).toBeNull()
  })

  it('leaves every key to a field the user is typing in', () => {
    expect(rowMoveIndex(stroke('j', { targetTag: 'INPUT' }), 0, 4)).toBeNull()
    expect(
      rowMoveIndex(stroke('ArrowDown', { targetEditable: true }), 0, 4),
    ).toBeNull()
  })

  it('answers null for a key that moves nothing and for an empty list', () => {
    expect(rowMoveIndex(stroke('Enter'), 0, 4)).toBeNull()
    expect(rowMoveIndex(stroke('x'), 0, 4)).toBeNull()
    expect(rowMoveIndex(stroke('ArrowDown'), 0, 0)).toBeNull()
  })
})

describe('activatesRow', () => {
  it('holds for a bare Enter only', () => {
    expect(activatesRow(stroke('Enter'))).toBe(true)
    expect(activatesRow(stroke('Enter', { metaKey: true }))).toBe(false)
    expect(activatesRow(stroke('Enter', { targetTag: 'TEXTAREA' }))).toBe(false)
    expect(activatesRow(stroke(' '))).toBe(false)
  })
})

describe('rowActionKey', () => {
  it('hands the row its key', () => {
    expect(rowActionKey(stroke('e'))).toBe('e')
    expect(rowActionKey(stroke('a'))).toBe('a')
  })

  it('keeps a key that already means something else', () => {
    expect(rowActionKey(stroke('e', { altKey: true }))).toBeNull()
    expect(rowActionKey(stroke('a', { targetTag: 'INPUT' }))).toBeNull()
    expect(rowActionKey(stroke('e', { isComposing: true }))).toBeNull()
  })
})
