// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { SHORTCUTS } from '@/lib/shortcuts'

import { AppShell } from './app-shell'

const { navigations, active } = vi.hoisted(() => ({
  navigations: [] as { to: string }[],
  active: { isAll: false, scoped: null as string | null, switcher: 0 },
}))

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => (opts: { to: string }) => navigations.push(opts),
}))
vi.mock('@/components/trau/active-repo', () => ({
  useActiveRepo: () => ({
    isAll: active.isAll,
    autoScope: () => (active.scoped ? { name: active.scoped } : null),
    openSwitcher: () => {
      active.switcher += 1
    },
  }),
}))
vi.mock('@/components/trau/sidebar', () => ({
  Sidebar: ({ onOpenPalette }: { onOpenPalette: () => void }) =>
    createElement(
      'button',
      { type: 'button', 'data-slot': 'open-palette', onClick: onOpenPalette },
      'palette',
    ),
}))
vi.mock('@/components/trau/command-palette', () => ({
  CommandPalette: ({ open }: { open: boolean }) =>
    open
      ? createElement(
          'div',
          { role: 'dialog', 'data-state': 'open' },
          'palette dialog',
        )
      : null,
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

function press(key: string, target: EventTarget = document.body) {
  act(() => {
    target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
  })
}

function shortcutRows(): HTMLElement[] {
  return [...document.body.querySelectorAll<HTMLElement>('[data-slot="shortcut-row"]')]
}

beforeEach(() => {
  navigations.length = 0
  active.isAll = false
  active.scoped = null
  active.switcher = 0
})

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

it('jumps to Backlog on g then b', () => {
  render()

  press('g')
  press('b')

  expect(navigations).toEqual([{ to: '/backlog', search: undefined }])
})

it('leaves an unknown second key alone and disarms', () => {
  render()

  press('g')
  press('z')
  press('b')

  expect(navigations).toEqual([])
})

it('stays inert while the user types in a field', () => {
  const container = render()
  const input = document.createElement('input')
  container.appendChild(input)

  press('g', input)
  press('b', input)

  expect(navigations).toEqual([])
})

it('stays inert while the palette is open', () => {
  const container = render()
  act(() => {
    container.querySelector<HTMLElement>('[data-slot="open-palette"]')?.click()
  })
  expect(
    document.querySelector('[role="dialog"][data-state="open"]'),
  ).not.toBeNull()

  press('g')
  press('b')

  expect(navigations).toEqual([])
})

it('takes the sidebar recovery for a gated page under All projects', () => {
  active.isAll = true
  render()

  press('g')
  press('l')

  expect(navigations).toEqual([])
  expect(active.switcher).toBe(1)
})

it('follows a gated jump once it can auto-scope', () => {
  active.isAll = true
  active.scoped = 'loop'
  render()

  press('g')
  press('l')

  expect(navigations).toEqual([{ to: '/loop', search: undefined }])
  expect(active.switcher).toBe(0)
})

it('opens the shortcuts dialog on ? with every registered binding', () => {
  render()

  press('?')

  const rows = shortcutRows()
  expect(rows).toHaveLength(SHORTCUTS.length)
  const text = rows.map((row) => row.textContent ?? '')
  expect(text.some((row) => row.includes('Open the command palette'))).toBe(
    true,
  )
  expect(text.some((row) => row.includes('Go to Backlog'))).toBe(true)
  expect(text.some((row) => row.includes('Next issue'))).toBe(true)
  expect(
    text.some((row) => row.includes("Open the highlighted ticket's actions")),
  ).toBe(true)
})

it('leaves ? alone while the palette is open, so no two dialogs stack', () => {
  const container = render()
  act(() => {
    container.querySelector<HTMLElement>('[data-slot="open-palette"]')?.click()
  })

  press('?')

  expect(shortcutRows()).toHaveLength(0)
  expect(
    document.querySelector('[role="dialog"][data-state="open"]'),
  ).not.toBeNull()
})
