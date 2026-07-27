// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'

import { shouldDodge, type Box } from './dock-dodge'

// The tab as it sits bottom-right of a 1280×800 viewport: 320 wide, 24 clear of both
// edges.
const TAB: Box = { top: 660, right: 1256, bottom: 776, left: 936 }

function field(html: string, box: Box): Element {
  document.body.innerHTML = html
  const el = document.body.firstElementChild as Element
  el.getBoundingClientRect = () =>
    ({
      ...box,
      x: box.left,
      y: box.top,
      width: box.right - box.left,
      height: box.bottom - box.top,
    }) as DOMRect
  return el
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('shouldDodge', () => {
  const over: Box = { top: 700, right: 1256, bottom: 760, left: 24 }
  const clear: Box = { top: 100, right: 1256, bottom: 160, left: 24 }

  it('dodges every kind of field the tab covers', () => {
    expect(shouldDodge(field('<textarea></textarea>', over), TAB)).toBe(true)
    expect(shouldDodge(field('<input />', over), TAB)).toBe(true)
    expect(shouldDodge(field('<input type="text" />', over), TAB)).toBe(true)
    expect(
      shouldDodge(field('<div contenteditable="true"></div>', over), TAB),
    ).toBe(true)
  })

  it('holds its corner for a field it does not cover', () => {
    expect(shouldDodge(field('<textarea></textarea>', clear), TAB)).toBe(false)
    expect(
      shouldDodge(field('<div contenteditable="true"></div>', clear), TAB),
    ).toBe(false)
  })

  it('counts a field that merely touches the tab as clear', () => {
    const edge: Box = { top: 700, right: 936, bottom: 760, left: 24 }
    expect(shouldDodge(field('<textarea></textarea>', edge), TAB)).toBe(false)
  })

  it('ignores what takes no keyboard', () => {
    expect(shouldDodge(field('<div></div>', over), TAB)).toBe(false)
    expect(shouldDodge(field('<button></button>', over), TAB)).toBe(false)
    expect(shouldDodge(field('<input type="hidden" />', over), TAB)).toBe(false)
    expect(
      shouldDodge(field('<div contenteditable="false"></div>', over), TAB),
    ).toBe(false)
  })

  it('holds its corner with nothing focused and no tab to measure', () => {
    expect(shouldDodge(null, TAB)).toBe(false)
    expect(shouldDodge(field('<textarea></textarea>', over), null)).toBe(false)
  })
})
