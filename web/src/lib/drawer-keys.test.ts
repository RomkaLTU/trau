// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'

import { drawerKeyAction, TICKET_DRAWER_LAYER } from './drawer-keys'
import { hasOpenLayer, readKeyStroke, soleOpenLayer, type KeyStroke } from './keys'

function stroke(key: string, over: Partial<KeyStroke> = {}): KeyStroke {
  return { key, soleLayer: TICKET_DRAWER_LAYER, ...over }
}

function openLayer(layer?: string): HTMLElement {
  const el = document.createElement('div')
  el.setAttribute('role', 'dialog')
  el.setAttribute('data-state', 'open')
  if (layer) el.setAttribute('data-key-layer', layer)
  document.body.appendChild(el)
  return el
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('soleOpenLayer', () => {
  it('names the one open layer', () => {
    openLayer(TICKET_DRAWER_LAYER)
    expect(soleOpenLayer(document)).toBe(TICKET_DRAWER_LAYER)
    expect(hasOpenLayer(document)).toBe(true)
  })

  it('names no layer while a second one is over it', () => {
    openLayer(TICKET_DRAWER_LAYER)
    openLayer()
    expect(soleOpenLayer(document)).toBeNull()
  })

  it('names no layer under an unnamed one, or under none at all', () => {
    expect(soleOpenLayer(document)).toBeNull()
    openLayer()
    expect(soleOpenLayer(document)).toBeNull()
  })

  it('is what a keystroke carries', () => {
    openLayer(TICKET_DRAWER_LAYER)
    const read = readKeyStroke(
      new KeyboardEvent('keydown', { key: 'j' }) as KeyboardEvent,
    )
    expect(read.soleLayer).toBe(TICKET_DRAWER_LAYER)
    expect(read.layerOpen).toBe(true)
  })
})

describe('drawerKeyAction', () => {
  it('walks the list under the open drawer', () => {
    expect(drawerKeyAction(stroke('j'))).toBe('next')
    expect(drawerKeyAction(stroke('k'))).toBe('prev')
  })

  it('gives the keys back to any other layer', () => {
    expect(drawerKeyAction(stroke('j', { soleLayer: null }))).toBeNull()
    expect(drawerKeyAction(stroke('j', { soleLayer: 'command-palette' }))).toBeNull()
  })

  it('leaves the keys as text in a field inside the drawer', () => {
    expect(drawerKeyAction(stroke('j', { targetTag: 'INPUT' }))).toBeNull()
    expect(drawerKeyAction(stroke('k', { targetEditable: true }))).toBeNull()
  })

  it('leaves a modified or composing stroke alone, a held Shift included', () => {
    expect(drawerKeyAction(stroke('j', { metaKey: true }))).toBeNull()
    expect(drawerKeyAction(stroke('k', { isComposing: true }))).toBeNull()
    expect(drawerKeyAction(stroke('j', { shiftKey: true }))).toBeNull()
  })

  it('claims no other key', () => {
    expect(drawerKeyAction(stroke('e'))).toBeNull()
    expect(drawerKeyAction(stroke('Escape'))).toBeNull()
  })
})
