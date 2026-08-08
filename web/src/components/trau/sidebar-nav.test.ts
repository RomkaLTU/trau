// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, createElement, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'

import { apiFetch } from '@/lib/api'

import { Sidebar } from './sidebar'

type LinkProps = {
  to: string
  search?: Record<string, string>
  activeOptions?: { exact?: boolean }
  activeProps?: Record<string, unknown>
  className?: string
  children: ReactNode | ((state: { isActive: boolean }) => ReactNode)
}

const { navigations, route, active } = vi.hoisted(() => ({
  navigations: [] as { to: string }[],
  route: { pathname: '/loop' },
  active: { isAll: false, switcher: 0 },
}))

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => (opts: { to: string }) => navigations.push(opts),
  useRouterState: ({
    select,
  }: {
    select: (state: { location: { pathname: string } }) => string
  }) => select({ location: { pathname: route.pathname } }),
  // Stand-in for <Link> that folds in activeProps only when the route matches,
  // so the aria-current under test is the sidebar's and not the router's.
  Link: ({
    to,
    search: _search,
    activeOptions,
    activeProps,
    className,
    children,
    ...rest
  }: LinkProps) => {
    const isActive = activeOptions?.exact
      ? route.pathname === to
      : route.pathname === to || route.pathname.startsWith(`${to}/`)
    const { className: activeClass, ...activeRest } = isActive
      ? (activeProps ?? {})
      : {}
    return createElement(
      'a',
      {
        href: to,
        className: [className, activeClass].filter(Boolean).join(' '),
        ...rest,
        ...activeRest,
      },
      typeof children === 'function' ? children({ isActive }) : children,
    )
  },
}))
vi.mock('@/components/trau/active-repo', () => ({
  useActiveRepo: () => ({
    repo: active.isAll ? null : 'loop',
    repos: [],
    isAll: active.isAll,
    autoScope: () => null,
    openSwitcher: () => {
      active.switcher += 1
    },
  }),
}))
vi.mock('@/components/trau/notification-bell', () => ({
  NotificationBell: () => null,
}))
vi.mock('@/components/trau/repo-switcher', () => ({ RepoSwitcher: () => null }))
vi.mock('@/components/trau/theme-toggle', () => ({ ThemeToggle: () => null }))
vi.mock('@/components/trau/whats-new-dialog', () => ({
  WhatsNewDialog: () => null,
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

let root: Root | undefined

function render() {
  vi.mocked(apiFetch).mockImplementation(() =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve({}),
    } as Response),
  )
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  act(() => {
    mounted.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(Sidebar, {
          onOpenPalette: () => {},
          onOpenSwitcher: () => {},
        }),
      ),
    )
  })
}

function nav(): HTMLElement {
  const found = document.body.querySelector('nav')
  if (!found) throw new Error('no sidebar nav rendered')
  return found
}

function navItems(): HTMLElement[] {
  return [...nav().querySelectorAll<HTMLElement>('a, button')]
}

function tabStop(): HTMLElement {
  const stops = navItems().filter((el) => el.tabIndex === 0)
  expect(stops).toHaveLength(1)
  return stops[0]
}

function label(el: Element | null): string {
  return el?.querySelector('span.flex-1')?.textContent ?? ''
}

function pressKey(key: string) {
  const target = document.activeElement ?? document.body
  act(() => {
    target.dispatchEvent(
      new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }),
    )
  })
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  navigations.length = 0
  route.pathname = '/loop'
  active.isAll = false
  active.switcher = 0
  vi.clearAllMocks()
})

it('names the navigation landmark and marks the route’s item', () => {
  render()

  expect(nav().getAttribute('aria-label')).toBe('Primary')
  const current = nav().querySelectorAll('[aria-current="page"]')
  expect(current).toHaveLength(1)
  expect(label(current[0])).toBe('Loop')
})

it('moves aria-current with the route', () => {
  route.pathname = '/settings'
  render()

  const current = nav().querySelectorAll('[aria-current="page"]')
  expect(current).toHaveLength(1)
  expect(label(current[0])).toBe('Settings')
})

it('offers one Tab stop, parked on the active route’s item', () => {
  render()

  expect(navItems()).toHaveLength(14)
  expect(label(tabStop())).toBe('Loop')
})

it('walks the whole nav with the arrow keys, clamping at both ends', () => {
  render()

  act(() => tabStop().focus())
  pressKey('ArrowDown')
  expect(label(document.activeElement)).toBe('Backlog')
  pressKey('ArrowUp')
  expect(label(document.activeElement)).toBe('Loop')

  pressKey('Home')
  expect(label(document.activeElement)).toBe('Overview')
  pressKey('ArrowUp')
  expect(label(document.activeElement)).toBe('Overview')

  pressKey('End')
  expect(label(document.activeElement)).toBe('Hub')
  pressKey('ArrowDown')
  expect(label(document.activeElement)).toBe('Hub')
})

it('reaches every gated item by arrow, disabled or not', () => {
  active.isAll = true
  route.pathname = '/'
  render()

  const seen: string[] = []
  act(() => tabStop().focus())
  seen.push(label(document.activeElement))
  for (let i = 0; i < 13; i += 1) {
    pressKey('ArrowDown')
    seen.push(label(document.activeElement))
  }

  expect(seen).toEqual(navItems().map((el) => label(el)))
  expect(seen).toContain('Terminal')
  expect(
    navItems().filter((el) => el.getAttribute('aria-disabled') === 'true'),
  ).not.toHaveLength(0)
})

it('activates the focused link with Enter and with Space', () => {
  render()

  const clicks: string[] = []
  for (const el of navItems()) {
    el.addEventListener('click', (event) => {
      event.preventDefault()
      clicks.push(label(el))
    })
  }

  act(() => tabStop().focus())
  pressKey('ArrowDown')
  pressKey('Enter')
  expect(clicks).toEqual(['Backlog'])

  pressKey(' ')
  expect(clicks).toEqual(['Backlog', 'Backlog'])
})

it('runs the recovery of a disabled item on Enter', () => {
  active.isAll = true
  route.pathname = '/'
  render()

  act(() => tabStop().focus())
  pressKey('ArrowDown')
  expect(label(document.activeElement)).toBe('Loop')
  expect(document.activeElement?.getAttribute('aria-disabled')).toBe('true')

  pressKey('Enter')
  expect(active.switcher).toBe(1)
  expect(navigations).toEqual([])
})

it('returns the Tab stop to the item last left off', () => {
  render()

  act(() => tabStop().focus())
  pressKey('ArrowDown')
  pressKey('ArrowDown')
  expect(label(document.activeElement)).toBe('Inbox')

  act(() => (document.activeElement as HTMLElement).blur())
  expect(label(tabStop())).toBe('Inbox')
})

it('mirrors the hover treatment on focus-visible', () => {
  render()

  const overview = navItems()[0]
  expect(overview.className).toContain('hover:bg-secondary')
  expect(overview.className).toContain('focus-visible:bg-secondary')
  expect(overview.className).toContain('focus-visible:text-foreground')
})
