import { describe, expect, it } from 'vitest'

import { inboxKeyAction } from './inbox-keys'

describe('inboxKeyAction', () => {
  it('maps the queue bindings', () => {
    expect(inboxKeyAction({ key: 'j' })).toBe('next')
    expect(inboxKeyAction({ key: 'k' })).toBe('prev')
    expect(inboxKeyAction({ key: 's' })).toBe('skip')
  })

  it('ignores unbound keys', () => {
    expect(inboxKeyAction({ key: 'x' })).toBeNull()
    expect(inboxKeyAction({ key: 'Enter' })).toBeNull()
    expect(inboxKeyAction({ key: 'ArrowDown' })).toBeNull()
  })

  it('leaves the shifted key alone', () => {
    expect(inboxKeyAction({ key: 'J' })).toBeNull()
  })

  it('yields a modified keystroke to the browser', () => {
    expect(inboxKeyAction({ key: 's', metaKey: true })).toBeNull()
    expect(inboxKeyAction({ key: 's', ctrlKey: true })).toBeNull()
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

  it('still fires over the page body', () => {
    expect(inboxKeyAction({ key: 'j', targetTag: 'BODY' })).toBe('next')
  })

  it('yields while a layer is open over the workspace', () => {
    expect(inboxKeyAction({ key: 'j', layerOpen: true })).toBeNull()
  })
})
