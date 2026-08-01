// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { ThemeToggle } from './theme-toggle'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  const items = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => items.get(key) ?? null,
    setItem: (key: string, value: string) => void items.set(key, value),
  })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

function checked(toggle: Element): string | null {
  return (
    toggle
      .querySelector('[role="radio"][aria-checked="true"]')
      ?.getAttribute('aria-label') ?? null
  )
}

// The palette's theme action and the sidebar toggle are two mounted consumers of
// one setting; a change in either has to move both, or the other one dead-locks
// on its own stale mode.
it('moves every consumer of the theme when one of them switches mode', () => {
  act(() => {
    root.render(
      createElement(
        'div',
        null,
        createElement(ThemeToggle),
        createElement(ThemeToggle),
      ),
    )
  })

  const [sidebar, elsewhere] = Array.from(
    container.querySelectorAll('[role="radiogroup"]'),
  )
  act(() => {
    sidebar.querySelector<HTMLButtonElement>('[aria-label="Dark"]')?.click()
  })

  expect(checked(sidebar)).toBe('Dark')
  expect(checked(elsewhere)).toBe('Dark')
  expect(document.documentElement.classList.contains('dark')).toBe(true)

  act(() => {
    elsewhere.querySelector<HTMLButtonElement>('[aria-label="Light"]')?.click()
  })

  expect(checked(sidebar)).toBe('Light')
  expect(document.documentElement.classList.contains('dark')).toBe(false)
})
