// @vitest-environment happy-dom
import { act, createElement, useRef, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'

import { useRovingList, type RovingList } from './use-roving-list'

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}
const scrolled = vi.spyOn(Element.prototype, 'scrollIntoView')

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

const opened: string[] = []
let list: RovingList | undefined
let root: Root | undefined

// Harness is the smallest list the primitive drives: rows carrying two controls
// each, which is what Tab has to step through.
function Harness({ ids }: { ids: string[] }): ReactNode {
  const container = useRef<HTMLDivElement | null>(null)
  const rows = useRovingList({
    container,
    onActivate: (id) => opened.push(id),
  })
  list = rows
  return createElement(
    'div',
    { ref: container, ...rows.listProps },
    ids.map((id, i) =>
      createElement(
        'div',
        {
          key: `${id}-${i}`,
          // A list that renders an id twice names each rendering, as the board
          // names one by its place in the tree.
          ...rows.rowProps(
            id,
            ids.indexOf(id) === ids.lastIndexOf(id) ? undefined : `${id}#${i}`,
          ),
          'aria-label': id,
        },
        createElement(
          'button',
          { ...rows.controlProps, 'data-name': `${id}-open` },
          'open',
        ),
        createElement(
          'button',
          { ...rows.controlProps, 'data-name': `${id}-archive` },
          'archive',
        ),
      ),
    ),
  )
}

function render(ids: string[]) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => mounted.render(createElement(Harness, { ids })))
  return (next: string[]) =>
    act(() => mounted.render(createElement(Harness, { ids: next })))
}

function rows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('[data-roving-row]')]
}

function stops(): HTMLElement[] {
  return rows().filter((el) => el.tabIndex === 0)
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

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  list = undefined
  opened.length = 0
  document.body.innerHTML = ''
  scrolled.mockClear()
})

it('offers one Tab stop, parked on the first row', () => {
  render(['A', 'B', 'C'])

  expect(rows()).toHaveLength(3)
  expect(stops()).toHaveLength(1)
  expect(stops()[0].getAttribute('aria-label')).toBe('A')
  expect(
    [...document.querySelectorAll('button')].every((el) => el.tabIndex === -1),
  ).toBe(true)
})

// A board renders one id twice whenever a child of an expanded parent also stands
// in its own section, so the stop and the movement both go by element.
it('keeps one Tab stop, on the row the reader is on, when an id renders twice', () => {
  render(['A', 'B', 'A'])

  expect(stops()).toHaveLength(1)

  act(() => rows()[2].focus())
  expect(stops()).toEqual([rows()[2]])

  press('ArrowUp')
  expect(document.activeElement).toBe(rows()[1])

  press('ArrowDown')
  expect(document.activeElement).toBe(rows()[2])
  expect(stops()).toEqual([rows()[2]])
})

it('walks the rows on the arrows and on a bare j/k, clamping at both ends', () => {
  render(['A', 'B', 'C'])

  act(() => rows()[0].focus())
  expect(press('ArrowDown')).toBe(true)
  expect(at()).toBe('B')
  press('j')
  expect(at()).toBe('C')
  press('ArrowDown')
  expect(at()).toBe('C')

  press('k')
  expect(at()).toBe('B')
  press('ArrowUp')
  expect(at()).toBe('A')
  press('ArrowUp')
  expect(at()).toBe('A')

  press('End')
  expect(at()).toBe('C')
  press('Home')
  expect(at()).toBe('A')
})

it('moves the Tab stop with the focused row and follows it into view', () => {
  render(['A', 'B', 'C'])

  act(() => rows()[0].focus())
  press('ArrowDown')

  expect(stops()).toHaveLength(1)
  expect(stops()[0].getAttribute('aria-label')).toBe('B')
  expect(scrolled).toHaveBeenCalledWith({ block: 'nearest' })
})

it('opens the focused row on Enter', () => {
  render(['A', 'B', 'C'])

  act(() => rows()[0].focus())
  press('ArrowDown')
  expect(press('Enter')).toBe(true)

  expect(opened).toEqual(['B'])
})

it('steps Tab into the row’s own controls, then out of the list', () => {
  render(['A', 'B'])

  act(() => rows()[0].focus())
  expect(press('Tab')).toBe(true)
  expect(at()).toBe('A-open')
  expect(press('Tab')).toBe(true)
  expect(at()).toBe('A-archive')

  // Past the last control the browser takes over, which is what leaves the list.
  expect(press('Tab')).toBe(false)
})

it('steps Shift+Tab back to the row, then out of the list', () => {
  render(['A', 'B'])

  act(() => rows()[0].focus())
  press('Tab')
  press('Tab')
  expect(press('Tab', { shiftKey: true })).toBe(true)
  expect(at()).toBe('A-open')
  expect(press('Tab', { shiftKey: true })).toBe(true)
  expect(at()).toBe('A')
  expect(press('Tab', { shiftKey: true })).toBe(false)
})

it('keeps moving rows while focus is on a control inside one', () => {
  render(['A', 'B', 'C'])

  act(() => rows()[0].focus())
  press('Tab')
  expect(at()).toBe('A-open')

  press('ArrowDown')
  expect(at()).toBe('B')
  expect(opened).toEqual([])
})

it('leaves Enter to the control it is pressed on', () => {
  render(['A', 'B'])

  act(() => rows()[0].focus())
  press('Tab')
  expect(press('Enter')).toBe(false)
  expect(opened).toEqual([])
})

it('returns the Tab stop to the first row when the row it sat on leaves', () => {
  const rerender = render(['A', 'B', 'C'])

  act(() => rows()[0].focus())
  press('ArrowDown')
  expect(stops()[0].getAttribute('aria-label')).toBe('B')

  rerender(['A', 'C'])
  expect(stops()).toHaveLength(1)
  expect(stops()[0].getAttribute('aria-label')).toBe('A')
})

it('focuses a row by id, and answers false for a row the list has not got', () => {
  render(['A', 'B', 'C'])

  act(() => {
    expect(list?.focusRow('C')).toBe(true)
  })
  expect(at()).toBe('C')
  expect(stops()[0].getAttribute('aria-label')).toBe('C')

  act(() => {
    expect(list?.focusRow('ZZ')).toBe(false)
  })
  expect(at()).toBe('C')
})

it('takes focus back onto the Tab stop row, never onto the body', () => {
  render(['A', 'B', 'C'])

  act(() => {
    list?.focusRow('B')
  })
  act(() => (document.activeElement as HTMLElement).blur())

  act(() => list?.focusTabStop())
  expect(at()).toBe('B')
  expect(document.activeElement).not.toBe(document.body)
})
