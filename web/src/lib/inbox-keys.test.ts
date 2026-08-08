import { describe, expect, it } from 'vitest'

import { inboxKeyAction, inboxMoveTarget } from './inbox-keys'
import type { InboxItem } from './inbox'

describe('inboxKeyAction', () => {
  it('maps the queue bindings', () => {
    expect(inboxKeyAction({ key: 'j' })).toBe('next')
    expect(inboxKeyAction({ key: 'k' })).toBe('prev')
  })

  it('walks with the arrows as well as the letters', () => {
    expect(inboxKeyAction({ key: 'ArrowDown' })).toBe('next')
    expect(inboxKeyAction({ key: 'ArrowUp' })).toBe('prev')
    expect(inboxKeyAction({ key: 'Home' })).toBe('first')
    expect(inboxKeyAction({ key: 'End' })).toBe('last')
  })

  it('ignores unbound keys', () => {
    expect(inboxKeyAction({ key: 'x' })).toBeNull()
    expect(inboxKeyAction({ key: 's' })).toBeNull()
    expect(inboxKeyAction({ key: 'Enter' })).toBeNull()
    expect(inboxKeyAction({ key: 'ArrowLeft' })).toBeNull()
  })

  it('leaves a shifted keystroke alone', () => {
    expect(inboxKeyAction({ key: 'J' })).toBeNull()
    expect(inboxKeyAction({ key: 'j', shiftKey: true })).toBeNull()
    expect(inboxKeyAction({ key: 'ArrowDown', shiftKey: true })).toBeNull()
  })

  it('yields a modified keystroke to the browser', () => {
    expect(inboxKeyAction({ key: 'j', metaKey: true })).toBeNull()
    expect(inboxKeyAction({ key: 'k', ctrlKey: true })).toBeNull()
    expect(inboxKeyAction({ key: 'j', altKey: true })).toBeNull()
  })

  it('yields to an IME composing the keystroke', () => {
    expect(inboxKeyAction({ key: 'j', isComposing: true })).toBeNull()
  })

  it('yields while focus is in a field', () => {
    expect(inboxKeyAction({ key: 'j', targetTag: 'TEXTAREA' })).toBeNull()
    expect(inboxKeyAction({ key: 'j', targetTag: 'INPUT' })).toBeNull()
    expect(inboxKeyAction({ key: 'j', targetTag: 'SELECT' })).toBeNull()
  })

  it('yields while focus is in a contenteditable editor', () => {
    expect(inboxKeyAction({ key: 'j', targetTag: 'DIV', targetEditable: true })).toBeNull()
    expect(inboxKeyAction({ key: 'k', targetTag: 'DIV', targetEditable: true })).toBeNull()
  })

  it('still fires over the page body', () => {
    expect(inboxKeyAction({ key: 'j', targetTag: 'BODY' })).toBe('next')
  })

  it('yields while a layer is open over the workspace', () => {
    expect(inboxKeyAction({ key: 'j', layerOpen: true })).toBeNull()
  })
})

function items(...ids: string[]): InboxItem[] {
  return ids.map((id) => ({ id, title: id, attention: 'open' }))
}

describe('inboxMoveTarget', () => {
  const queue = items('A', 'B', 'C')

  it('steps the queue', () => {
    expect(inboxMoveTarget(queue, 'A', 'next')).toBe('B')
    expect(inboxMoveTarget(queue, 'B', 'prev')).toBe('A')
  })

  it('stops at the ends rather than wrapping', () => {
    expect(inboxMoveTarget(queue, 'C', 'next')).toBeNull()
    expect(inboxMoveTarget(queue, 'A', 'prev')).toBeNull()
  })

  it('jumps to either end', () => {
    expect(inboxMoveTarget(queue, 'B', 'first')).toBe('A')
    expect(inboxMoveTarget(queue, 'B', 'last')).toBe('C')
  })

  it('starts the walk at the head when nothing is selected', () => {
    expect(inboxMoveTarget(queue, null, 'next')).toBe('A')
    expect(inboxMoveTarget(queue, null, 'prev')).toBe('A')
  })

  it('goes nowhere from a row the walk does not hold', () => {
    expect(inboxMoveTarget(queue, 'DONE-1', 'next')).toBeNull()
    expect(inboxMoveTarget(queue, 'DONE-1', 'prev')).toBeNull()
  })

  it('has nowhere to go in an empty queue', () => {
    expect(inboxMoveTarget([], null, 'next')).toBeNull()
    expect(inboxMoveTarget([], null, 'first')).toBeNull()
  })
})
