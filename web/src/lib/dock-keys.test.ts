import { describe, expect, it } from 'vitest'

import { dockPanelAction, isDockOpenKey } from './dock-keys'

describe('isDockOpenKey', () => {
  it('matches the global binding', () => {
    expect(isDockOpenKey({ key: 'i' })).toBe(true)
    expect(isDockOpenKey({ key: 'i', targetTag: 'BODY' })).toBe(true)
  })

  it('ignores every other key', () => {
    expect(isDockOpenKey({ key: 'I' })).toBe(false)
    expect(isDockOpenKey({ key: 'j' })).toBe(false)
    expect(isDockOpenKey({ key: 'Enter' })).toBe(false)
  })

  it('yields a modified keystroke to the browser', () => {
    expect(isDockOpenKey({ key: 'i', metaKey: true })).toBe(false)
    expect(isDockOpenKey({ key: 'i', ctrlKey: true })).toBe(false)
    expect(isDockOpenKey({ key: 'i', altKey: true })).toBe(false)
  })

  it('yields to an IME composing the keystroke', () => {
    expect(isDockOpenKey({ key: 'i', isComposing: true })).toBe(false)
  })

  it('yields while the user is typing', () => {
    expect(isDockOpenKey({ key: 'i', targetTag: 'INPUT' })).toBe(false)
    expect(isDockOpenKey({ key: 'i', targetTag: 'TEXTAREA' })).toBe(false)
    expect(isDockOpenKey({ key: 'i', targetTag: 'SELECT' })).toBe(false)
    expect(
      isDockOpenKey({ key: 'i', targetTag: 'DIV', targetEditable: true }),
    ).toBe(false)
  })

  it('yields while a layer is open over the workspace', () => {
    expect(isDockOpenKey({ key: 'i', layerOpen: true })).toBe(false)
  })
})

describe('dockPanelAction', () => {
  it('maps the panel bindings', () => {
    expect(dockPanelAction({ key: 'j' })).toBe('next')
    expect(dockPanelAction({ key: 'ArrowDown' })).toBe('next')
    expect(dockPanelAction({ key: 'k' })).toBe('prev')
    expect(dockPanelAction({ key: 'ArrowUp' })).toBe('prev')
    expect(dockPanelAction({ key: 'Enter' })).toBe('answer')
    expect(dockPanelAction({ key: 'x' })).toBe('abandon')
    expect(dockPanelAction({ key: 'Escape' })).toBe('collapse')
  })

  it('ignores unbound keys', () => {
    expect(dockPanelAction({ key: 'i' })).toBeNull()
    expect(dockPanelAction({ key: 's' })).toBeNull()
    expect(dockPanelAction({ key: 'X' })).toBeNull()
  })

  it('yields a modified keystroke to the browser', () => {
    expect(dockPanelAction({ key: 'j', metaKey: true })).toBeNull()
    expect(dockPanelAction({ key: 'k', ctrlKey: true })).toBeNull()
    expect(dockPanelAction({ key: 'Enter', altKey: true })).toBeNull()
  })

  it('yields to an IME composing the keystroke', () => {
    expect(dockPanelAction({ key: 'j', isComposing: true })).toBeNull()
    expect(dockPanelAction({ key: 'Escape', isComposing: true })).toBeNull()
  })

  it('leaves the answer box its typing', () => {
    const box = { targetTag: 'DIV', targetEditable: true }
    expect(dockPanelAction({ key: 'j', ...box })).toBeNull()
    expect(dockPanelAction({ key: 'k', ...box })).toBeNull()
    expect(dockPanelAction({ key: 'x', ...box })).toBeNull()
    expect(dockPanelAction({ key: 'Enter', ...box })).toBeNull()
    expect(dockPanelAction({ key: 'j', targetTag: 'INPUT' })).toBeNull()
  })

  it('collapses from the answer box, which is never typing Escape', () => {
    expect(
      dockPanelAction({ key: 'Escape', targetTag: 'DIV', targetEditable: true }),
    ).toBe('collapse')
    expect(dockPanelAction({ key: 'Escape', targetTag: 'INPUT' })).toBe(
      'collapse',
    )
  })

  it('yields while a layer is open over the panel', () => {
    expect(dockPanelAction({ key: 'j', layerOpen: true })).toBeNull()
    expect(dockPanelAction({ key: 'Escape', layerOpen: true })).toBeNull()
  })
})
