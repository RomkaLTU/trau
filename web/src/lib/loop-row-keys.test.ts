import { describe, expect, it } from 'vitest'

import type { KeyStroke } from './keys'
import { loopRowAction } from './loop-row-keys'

function stroke(over: Partial<KeyStroke> & { key: string }): KeyStroke {
  return { ...over }
}

describe('loopRowAction', () => {
  it('binds the row’s single-key actions', () => {
    expect(loopRowAction(stroke({ key: ' ' }))).toBe('select')
    expect(loopRowAction(stroke({ key: 'r' }))).toBe('run')
    expect(loopRowAction(stroke({ key: 'x' }))).toBe('remove')
  })

  it('reorders on the Alt arrows only', () => {
    expect(loopRowAction(stroke({ key: 'ArrowUp', altKey: true }))).toBe('moveUp')
    expect(loopRowAction(stroke({ key: 'ArrowDown', altKey: true }))).toBe(
      'moveDown',
    )
    // A bare arrow is the list's walk, never the row's order.
    expect(loopRowAction(stroke({ key: 'ArrowUp' }))).toBeNull()
    expect(loopRowAction(stroke({ key: 'ArrowDown' }))).toBeNull()
  })

  it('leaves a stroke carrying another modifier alone', () => {
    expect(
      loopRowAction(stroke({ key: 'ArrowUp', altKey: true, shiftKey: true })),
    ).toBeNull()
    expect(
      loopRowAction(stroke({ key: 'ArrowDown', altKey: true, metaKey: true })),
    ).toBeNull()
    expect(loopRowAction(stroke({ key: 'r', metaKey: true }))).toBeNull()
    expect(loopRowAction(stroke({ key: 'x', ctrlKey: true }))).toBeNull()
    expect(loopRowAction(stroke({ key: 'r', shiftKey: true }))).toBeNull()
  })

  it('goes inert while the row is being typed in', () => {
    expect(loopRowAction(stroke({ key: 'x', targetTag: 'INPUT' }))).toBeNull()
    expect(loopRowAction(stroke({ key: ' ', targetTag: 'TEXTAREA' }))).toBeNull()
    expect(loopRowAction(stroke({ key: 'r', targetEditable: true }))).toBeNull()
    expect(
      loopRowAction(stroke({ key: 'ArrowUp', altKey: true, targetTag: 'INPUT' })),
    ).toBeNull()
  })

  it('leaves an IME composition to the IME', () => {
    expect(loopRowAction(stroke({ key: 'r', isComposing: true }))).toBeNull()
    expect(
      loopRowAction(stroke({ key: 'ArrowDown', altKey: true, isComposing: true })),
    ).toBeNull()
  })

  it('claims no other key', () => {
    expect(loopRowAction(stroke({ key: 'Enter' }))).toBeNull()
    expect(loopRowAction(stroke({ key: 'j' }))).toBeNull()
    expect(loopRowAction(stroke({ key: 'e' }))).toBeNull()
    expect(loopRowAction(stroke({ key: 'Tab' }))).toBeNull()
  })
})
