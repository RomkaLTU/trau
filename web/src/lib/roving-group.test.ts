import { describe, expect, it } from 'vitest'

import { type KeyStroke } from './keys'
import { groupMoveIndex, wrapIndex } from './roving-group'

function stroke(key: string, over: Partial<KeyStroke> = {}): KeyStroke {
  return { key, ...over }
}

describe('wrapIndex', () => {
  it('wraps around both ends', () => {
    expect(wrapIndex(0, 3)).toBe(0)
    expect(wrapIndex(3, 3)).toBe(0)
    expect(wrapIndex(-1, 3)).toBe(2)
    expect(wrapIndex(-4, 3)).toBe(2)
  })

  it('answers null for a group with no items', () => {
    expect(wrapIndex(0, 0)).toBeNull()
    expect(wrapIndex(1, -2)).toBeNull()
  })
})

describe('groupMoveIndex', () => {
  it('steps forward on either forward arrow', () => {
    expect(groupMoveIndex(stroke('ArrowRight'), 0, 3)).toBe(1)
    expect(groupMoveIndex(stroke('ArrowDown'), 1, 3)).toBe(2)
  })

  it('steps back on either back arrow', () => {
    expect(groupMoveIndex(stroke('ArrowLeft'), 2, 3)).toBe(1)
    expect(groupMoveIndex(stroke('ArrowUp'), 1, 3)).toBe(0)
  })

  it('wraps at both ends', () => {
    expect(groupMoveIndex(stroke('ArrowRight'), 2, 3)).toBe(0)
    expect(groupMoveIndex(stroke('ArrowLeft'), 0, 3)).toBe(2)
  })

  it('jumps to the ends on Home and End', () => {
    expect(groupMoveIndex(stroke('Home'), 2, 3)).toBe(0)
    expect(groupMoveIndex(stroke('End'), 0, 3)).toBe(2)
  })

  it('holds still on a group of one', () => {
    expect(groupMoveIndex(stroke('ArrowRight'), 0, 1)).toBe(0)
    expect(groupMoveIndex(stroke('ArrowLeft'), 0, 1)).toBe(0)
  })

  it('answers null for a group with no items', () => {
    expect(groupMoveIndex(stroke('ArrowRight'), 0, 0)).toBeNull()
    expect(groupMoveIndex(stroke('Home'), 0, 0)).toBeNull()
  })

  it('leaves every other key alone', () => {
    expect(groupMoveIndex(stroke('Enter'), 0, 3)).toBeNull()
    expect(groupMoveIndex(stroke(' '), 0, 3)).toBeNull()
    expect(groupMoveIndex(stroke('Tab'), 0, 3)).toBeNull()
    expect(groupMoveIndex(stroke('j'), 0, 3)).toBeNull()
  })

  it('leaves a modified arrow to the browser', () => {
    expect(groupMoveIndex(stroke('ArrowRight', { metaKey: true }), 0, 3)).toBeNull()
    expect(groupMoveIndex(stroke('ArrowRight', { ctrlKey: true }), 0, 3)).toBeNull()
    expect(groupMoveIndex(stroke('ArrowRight', { altKey: true }), 0, 3)).toBeNull()
    expect(groupMoveIndex(stroke('ArrowRight', { shiftKey: true }), 0, 3)).toBeNull()
    expect(
      groupMoveIndex(stroke('ArrowRight', { isComposing: true }), 0, 3),
    ).toBeNull()
  })

  it('leaves the arrows to a field inside the group', () => {
    expect(
      groupMoveIndex(stroke('ArrowRight', { targetTag: 'INPUT' }), 0, 3),
    ).toBeNull()
    expect(
      groupMoveIndex(stroke('Home', { targetEditable: true }), 0, 3),
    ).toBeNull()
  })
})
