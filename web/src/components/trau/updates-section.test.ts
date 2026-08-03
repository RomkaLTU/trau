// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { ReleaseNotes } from '@/components/trau/updates-section'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

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

function render(latest: string, notes: string): HTMLElement {
  act(() => root.render(createElement(ReleaseNotes, { latest, notes })))
  return container.firstElementChild as HTMLElement
}

function changelogLink(): HTMLAnchorElement | undefined {
  return [...container.querySelectorAll('a')].find(
    (el) => el.getAttribute('href') === 'https://trau.sh/changelog',
  )
}

it('renders the notes for the latest tag as markdown', () => {
  const block = render('2.3.0', '## Fixes\n\n- a `hub` restart no longer stalls')

  expect(block.textContent).toContain("What's new in v2.3.0")
  expect(block.querySelector('code')?.textContent).toBe('hub')
  expect(block.querySelector('li')?.textContent).toContain('restart no longer')
})

it('scrolls a long body instead of letting it swallow the page', () => {
  const box = render('2.3.0', 'line\n\nline').querySelector('div')

  expect(box?.className).toContain('max-h-64')
  expect(box?.className).toContain('overflow-y-auto')
})

it('keeps the changelog link when there are no notes to show', () => {
  const withoutBody = render('2.3.0', '')
  expect(withoutBody.textContent).not.toContain("What's new")
  expect(changelogLink()?.textContent).toBe('https://trau.sh/changelog')

  const unchecked = render('', '')
  expect(unchecked.textContent).not.toContain("What's new")
  expect(changelogLink()?.textContent).toBe('https://trau.sh/changelog')
})
