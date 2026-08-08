// @vitest-environment happy-dom
import { act, createElement, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'

import type { InboxItem } from './inbox'
import { useInboxRail } from './inbox-rail'

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}
const scrolled = vi.spyOn(Element.prototype, 'scrollIntoView')

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

let selection: string | null = null
let root: Root | undefined

// Harness is the rail reduced to what the walk touches: the queue's own rows, the
// Done today rows rendered below them, and two controls per row for Tab to step
// through. The selection lives above the hook, as the URL param does on the page.
function Harness({
  ids,
  done = [],
  initial,
}: {
  ids: string[]
  done?: string[]
  initial: string | null
}): ReactNode {
  const [selectedId, setSelectedId] = useState(initial)
  const items: InboxItem[] = ids.map((id) => ({
    id,
    title: id,
    attention: 'open',
  }))
  const rail = useInboxRail({ items, selectedId, onSelect: setSelectedId })
  selection = selectedId
  return createElement(
    'nav',
    { ref: rail.listRef, ...rail.listProps },
    [...ids, ...done].map((id) =>
      createElement(
        'li',
        { key: id, ...rail.rowProps(id), 'aria-label': id },
        createElement(
          'button',
          {
            ...rail.controlProps,
            'data-name': `${id}-open`,
            onClick: () => setSelectedId(id),
          },
          'open',
        ),
        createElement(
          'button',
          { ...rail.controlProps, 'data-name': `${id}-ask` },
          'ask',
        ),
      ),
    ),
  )
}

function render(props: { ids: string[]; done?: string[]; initial: string | null }) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => mounted.render(createElement(Harness, props)))
}

function rows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('[data-roving-row]')]
}

function stops(): string[] {
  return rows()
    .filter((el) => el.tabIndex === 0)
    .map((el) => el.getAttribute('aria-label') ?? '')
}

function row(id: string): HTMLElement {
  return document.querySelector<HTMLElement>(`[data-roving-row="${id}"]`)!
}

function control(name: string): HTMLElement {
  return document.querySelector<HTMLElement>(`[data-name="${name}"]`)!
}

function at(): string {
  const el = document.activeElement as HTMLElement | null
  return el?.getAttribute('aria-label') ?? el?.dataset.name ?? 'none'
}

function press(key: string, init: KeyboardEventInit = {}): boolean {
  const target = document.activeElement ?? document.body
  const event = new KeyboardEvent('keydown', {
    key,
    bubbles: true,
    cancelable: true,
    ...init,
  })
  act(() => {
    target.dispatchEvent(event)
  })
  return event.defaultPrevented
}

function layer(name?: string) {
  const el = document.createElement('div')
  el.setAttribute('role', 'dialog')
  el.setAttribute('data-state', 'open')
  if (name) el.setAttribute('data-key-layer', name)
  document.body.appendChild(el)
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  selection = null
  document.body.innerHTML = ''
  scrolled.mockClear()
})

it('parks the one Tab stop on the selected row', () => {
  render({ ids: ['A', 'B', 'C'], initial: 'B' })

  expect(rows()).toHaveLength(3)
  expect(stops()).toEqual(['B'])
})

it('moves selection, focus and scroll together', () => {
  render({ ids: ['A', 'B', 'C'], initial: 'A' })
  act(() => row('A').focus())
  scrolled.mockClear()

  expect(press('j')).toBe(true)
  expect(selection).toBe('B')
  expect(at()).toBe('B')
  expect(stops()).toEqual(['B'])
  expect(scrolled).toHaveBeenCalled()

  press('k')
  expect(selection).toBe('A')
  expect(at()).toBe('A')
})

it('walks with the arrows and jumps with Home and End', () => {
  render({ ids: ['A', 'B', 'C'], initial: 'A' })
  act(() => row('A').focus())

  press('ArrowDown')
  expect(selection).toBe('B')
  press('ArrowUp')
  expect(selection).toBe('A')
  press('End')
  expect(selection).toBe('C')
  expect(at()).toBe('C')
  press('Home')
  expect(selection).toBe('A')
})

it('stops at the ends rather than wrapping', () => {
  render({ ids: ['A', 'B'], initial: 'B' })
  act(() => row('B').focus())

  expect(press('j')).toBe(true)
  expect(selection).toBe('B')
  expect(at()).toBe('B')
})

it('leaves a shifted keystroke to the browser', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  act(() => row('A').focus())

  expect(press('j', { shiftKey: true })).toBe(false)
  expect(press('ArrowDown', { shiftKey: true })).toBe(false)
  expect(selection).toBe('A')
  expect(at()).toBe('A')
})

it('keeps the Done today rows out of the walk', () => {
  render({ ids: ['A', 'B'], done: ['DONE-1'], initial: 'B' })
  act(() => row('B').focus())

  expect(rows()).toHaveLength(3)
  expect(press('j')).toBe(true)
  expect(selection).toBe('B')
  expect(at()).toBe('B')
})

it('opens the selected row on Enter', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  act(() => row('A').focus())

  expect(press('Enter')).toBe(true)
  expect(selection).toBe('A')
})

it('steps into the row’s own controls on Tab', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  act(() => row('A').focus())

  expect(press('Tab')).toBe(true)
  expect(at()).toBe('A-open')
  press('Tab')
  expect(at()).toBe('A-ask')
})

it('walks on from a control inside the focused row', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  act(() => control('A-open').focus())

  press('j')
  expect(selection).toBe('B')
  expect(at()).toBe('B')
})

it('syncs focus onto the row a click selected', () => {
  render({ ids: ['A', 'B'], initial: 'A' })

  act(() => {
    control('B-open').focus()
    control('B-open').click()
  })

  expect(selection).toBe('B')
  expect(at()).toBe('B')
  expect(stops()).toEqual(['B'])
})

it('walks from outside the rail without taking the focus', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  scrolled.mockClear()

  expect(press('j')).toBe(true)
  expect(selection).toBe('B')
  expect(at()).toBe('none')
  expect(scrolled).toHaveBeenCalled()
})

it('yields while focus is in a field', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  const field = document.createElement('textarea')
  document.body.appendChild(field)
  act(() => field.focus())

  expect(press('j')).toBe(false)
  expect(selection).toBe('A')
})

it('yields to a layer over the workspace', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  layer()

  expect(press('j')).toBe(false)
  expect(selection).toBe('A')
})

it('keeps walking under an open ticket drawer', () => {
  render({ ids: ['A', 'B'], initial: 'A' })
  layer('ticket-drawer')

  expect(press('j')).toBe(true)
  expect(selection).toBe('B')
})

it('scrolls the row an outside advance selected into view', () => {
  render({ ids: ['A', 'B', 'C'], initial: 'A' })
  act(() => control('C-open').blur())
  scrolled.mockClear()

  press('End')
  expect(selection).toBe('C')
  expect(scrolled).toHaveBeenCalled()
  expect(at()).toBe('none')
})
