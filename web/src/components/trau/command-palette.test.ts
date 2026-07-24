// @vitest-environment happy-dom
import { act, createElement } from 'react'
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

function selectedValue(): string | null {
  const el = container.querySelector('[cmdk-item=""][aria-selected="true"]')
  return el?.getAttribute('data-value') ?? null
}

function arrowDown() {
  const input = container.querySelector('[cmdk-input=""]') as HTMLInputElement
  act(() => {
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
}

it('steps keyboard selection through repos that share a name', () => {
  act(() => {
    root.render(
      createElement(
        Command,
        null,
        createElement(CommandInput, null),
        createElement(
          CommandList,
          null,
          createElement(
            CommandGroup,
            { heading: 'Projects' },
            repos.map((r) =>
              createElement(
                CommandItem,
                { key: r.root, value: r.root, keywords: [r.name] },
                r.name,
              ),
            ),
          ),
        ),
      ),
    )
  })

  expect(selectedValue()).toBe('/Users/rd/Projects/loop')
  arrowDown()
  expect(selectedValue()).toBe('/private/tmp/a')
  arrowDown()
  expect(selectedValue()).toBe('/private/tmp/b')
})
