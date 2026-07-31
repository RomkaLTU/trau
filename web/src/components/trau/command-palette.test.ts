// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, createElement, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { apiFetch } from '@/lib/api'

import { CommandPalette } from './command-palette'

const { navigations } = vi.hoisted(() => ({
  navigations: [] as { to: string; search?: { q?: string } }[],
}))

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => (opts: { to: string; search?: { q?: string } }) =>
    navigations.push(opts),
  useRouterState: ({
    select,
  }: {
    select: (state: { location: { pathname: string } }) => string
  }) => select({ location: { pathname: '/loop' } }),
}))
vi.mock('@/components/trau/active-repo', () => ({
  ALL_SCOPE: 'All repos',
  useActiveRepo: () => ({
    scope: 'loop',
    repo: 'loop',
    isAll: false,
    repos: [],
    setScope: () => {},
    setRepo: () => {},
    autoScope: () => null,
    openSwitcher: () => {},
    switcherSignal: 0,
  }),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

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
  navigations.length = 0
  vi.mocked(apiFetch).mockReset()
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

const configKeys = [
  {
    key: 'LINEAR_API_KEY',
    value: 'lin_api_notsoserious',
    layer: 'user',
    group: 'Tracker & issues',
    editable: true,
    secret: true,
    set: true,
  },
  {
    key: 'BASE_BRANCH',
    value: 'main',
    layer: 'default',
    group: 'Git & merge',
    editable: true,
  },
]

function renderPalette() {
  const opens: boolean[] = []
  vi.mocked(apiFetch).mockImplementation((input: string) =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve(
          input.includes('/config')
            ? { repo: 'loop', layers: ['user'], providers: [], keys: configKeys }
            : { instances: [] },
        ),
    } as Response),
  )
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  act(() => {
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(CommandPalette, {
          open: true,
          onOpenChange: (open: boolean) => opens.push(open),
        }),
      ),
    )
  })
  return opens
}

function typeQuery(text: string) {
  const input = document.body.querySelector(
    '[data-slot="command-input"]',
  ) as HTMLInputElement
  const setValue = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )?.set as (this: HTMLInputElement, value: string) => void
  act(() => {
    setValue.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

it('lists matching settings keys and lands on the settings page', async () => {
  const opens = renderPalette()
  typeQuery('linear')
  await act(async () => {})

  const row = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="setting:LINEAR_API_KEY"]',
  )
  expect(row?.textContent).toContain('LINEAR_API_KEY')
  expect(row?.textContent).toContain('Tracker & issues')
  expect(row?.textContent).toContain('••••••••')
  expect(document.body.textContent).not.toContain('lin_api_notsoserious')

  act(() => row?.click())

  expect(navigations).toEqual([
    { to: '/settings', search: { q: 'LINEAR_API_KEY' } },
  ])
  expect(opens).toEqual([false])
})
