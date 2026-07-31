// @vitest-environment happy-dom
import { act, createElement, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

// Two repos share the name "repo"; cmdk keys selection off each item's value, so
// the value must be the unique root — otherwise arrow-down stalls on the collision.
const repos = [
  { name: 'loop', root: '/Users/rd/Projects/loop' },
  { name: 'repo', root: '/private/tmp/a' },
  { name: 'repo', root: '/private/tmp/b' },
]

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function render(query: string, items: ReactNode) {
  act(() => {
    root.render(
      createElement(
        Command,
        { shouldFilter: false },
        createElement(CommandInput, { detachSearch: true, value: query }),
        createElement(
          CommandList,
          null,
          createElement(CommandGroup, { heading: 'Issues' }, items),
        ),
      ),
    )
  })
}

function itemValues(): string[] {
  return Array.from(container.querySelectorAll('[cmdk-item=""]')).map(
    (el) => el.getAttribute('data-value') ?? '',
  )
}

function selectedValue(): string | null {
  const el = container.querySelector('[cmdk-item=""][aria-selected="true"]')
  return el?.getAttribute('data-value') ?? null
}

function arrowDown() {
  const input = container.querySelector('input') as HTMLInputElement
  act(() => {
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
}

it('steps keyboard selection through repos that share a name', () => {
  render(
    '',
    repos.map((r) =>
      createElement(CommandItem, { key: r.root, value: r.root }, r.name),
    ),
  )

  expect(selectedValue()).toBe('/Users/rd/Projects/loop')
  arrowDown()
  expect(selectedValue()).toBe('/private/tmp/a')
  arrowDown()
  expect(selectedValue()).toBe('/private/tmp/b')
})

// The hub ranks issue hits; cmdk would re-sort them by its own score and drop
// anything it scores at zero, so the palette turns that filtering off.
it('renders server-ranked issue rows in the order they arrived', () => {
  const ranked = ['issue:COD-9', 'issue:COD-1340', 'issue:COD-31']
  render(
    'palette modal',
    ranked.map((value) => createElement(CommandItem, { key: value, value }, value)),
  )

  expect(itemValues()).toEqual(ranked)
})

it('leaves the highlight alone while the query is edited', () => {
  const items = ['issue:COD-9', 'issue:COD-1340', 'issue:COD-31'].map((value) =>
    createElement(CommandItem, { key: value, value }, value),
  )

  render('palette', items)
  arrowDown()
  expect(selectedValue()).toBe('issue:COD-1340')

  render('palett', items)
  expect(selectedValue()).toBe('issue:COD-1340')
})
