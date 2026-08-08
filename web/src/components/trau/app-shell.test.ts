// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'

import { AppShell } from './app-shell'

vi.mock('@/components/trau/sidebar', () => ({ Sidebar: () => null }))
vi.mock('@/components/trau/command-palette', () => ({
  CommandPalette: () => null,
}))
vi.mock('@/components/trau/repo-switcher', () => ({
  RepoSwitcherDialog: () => null,
}))
vi.mock('@/components/trau/recents-tracker', () => ({
  RecentsTracker: () => null,
}))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

let root: Root | undefined

function render() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => {
    mounted.render(
      createElement(AppShell, null, createElement('p', null, 'page')),
    )
  })
  return container
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

it('opens the shell with a skip link pointing at the main region', () => {
  const container = render()

  const skip = container.querySelector('a')
  expect(skip?.textContent).toBe('Skip to content')
  expect(skip?.getAttribute('href')).toBe('#main')
  expect(skip).toBe(container.firstElementChild?.firstElementChild)
  expect(skip?.className).toContain('sr-only')
  expect(skip?.className).toContain('focus:not-sr-only')
})

it('makes the main region a focus target for the skip link', () => {
  const container = render()

  const main = container.querySelector('main')
  expect(main?.id).toBe('main')
  expect(main?.tabIndex).toBe(-1)

  act(() => main?.focus())
  expect(document.activeElement).toBe(main)
})
