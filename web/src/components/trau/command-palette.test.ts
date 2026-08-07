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

type Navigation = {
  to: string
  search?: { q?: string }
  hash?: string
  params?: { repo: string; ticket: string }
}

const { navigations, active } = vi.hoisted(() => ({
  navigations: [] as Navigation[],
  active: {
    isAll: false,
    picked: [] as string[],
    switcher: 0,
    repos: [] as { name: string; root: string }[],
  },
}))

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => (opts: Navigation) => navigations.push(opts),
  useRouterState: ({
    select,
  }: {
    select: (state: { location: { pathname: string } }) => string
  }) => select({ location: { pathname: '/loop' } }),
}))
vi.mock('@/components/trau/active-repo', () => ({
  ALL_SCOPE: 'All repos',
  useActiveRepo: () => ({
    scope: active.isAll ? 'All repos' : 'loop',
    repo: active.isAll ? null : 'loop',
    isAll: active.isAll,
    repos: active.repos,
    setScope: (next: string) => active.picked.push(next),
    setRepo: () => {},
    autoScope: () => null,
    openSwitcher: () => {
      active.switcher += 1
    },
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
  active.isAll = false
  active.picked.length = 0
  active.switcher = 0
  active.repos = []
  hub.draining = false
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
  {
    key: 'LOOP_MAX_ATTEMPTS',
    value: '3',
    layer: 'default',
    group: 'Loop & queue',
    editable: true,
  },
]

const ledgerRuns = [
  {
    ticket: 'COD-1342',
    title: 'Runs search in the palette',
    phase: 'pr_open',
    phase_rank: 5,
    terminal: false,
    updated_at: new Date(Date.now() - 90 * 60 * 1000).toISOString(),
  },
  {
    ticket: 'COD-31',
    title: 'Costs page',
    phase: 'merged',
    phase_rank: 6,
    terminal: true,
    updated_at: '2026-07-01T09:00:00Z',
  },
  {
    ticket: 'COD-1351',
    title: 'Loop card polish',
    phase: 'pr_open',
    phase_rank: 5,
    terminal: false,
    updated_at: '2026-07-02T09:00:00Z',
  },
]

const globalResults = {
  issues: [
    {
      id: 'COD-1343',
      title: 'Cross-repo palette search',
      status: 'In Progress',
      group: 'started',
      source: 'linear',
      labels: [],
      has_children: false,
      repo: 'loop',
    },
  ],
  settings: [
    {
      repo: 'atlas',
      key: 'LINEAR_API_KEY',
      group: 'Tracker & issues',
      value: '••••••••',
    },
  ],
  runs: [
    {
      ticket: 'COD-31',
      title: 'Costs page',
      phase: 'merged',
      phase_rank: 6,
      terminal: true,
      updated_at: '2026-07-01T09:00:00Z',
      repo: 'atlas',
    },
  ],
}

const issueHits = [
  {
    id: 'COD-1345',
    title: 'Per-ticket actions submenu',
    status: 'Todo',
    group: 'unstarted',
    source: 'linear',
    labels: [],
    has_children: false,
  },
]

const hub = { draining: false }

function payloadFor(url: string) {
  if (url.startsWith('/api/v1/search')) {
    return { query: 'palette', results: globalResults }
  }
  if (url.includes('/issues/search')) {
    return { repo: 'loop', query: 'palette', results: issueHits }
  }
  if (url.includes('/archive')) {
    return { id: 'COD-1343', archived: true, queue_removed: 1 }
  }
  if (url.includes('/update/check')) {
    return {
      running: 'v0.9.0',
      onDisk: 'v0.9.0',
      latest: 'v0.9.1',
      updateAvailable: true,
    }
  }
  if (url.includes('/config')) {
    return { repo: 'loop', layers: ['user'], providers: [], keys: configKeys }
  }
  if (url.includes('/runs')) return { repo: 'loop', runs: ledgerRuns }
  if (url.includes('/queue')) {
    return { repo: 'loop', draining: hub.draining, stopping: false, items: [] }
  }
  if (url.includes('/health')) {
    return { repo: 'loop', provider: 'linear', state: 'ok' }
  }
  return { instances: [] }
}

function requestedUrls(): string[] {
  return vi.mocked(apiFetch).mock.calls.map(([url]) => url)
}

function sentWith(method: string): string[] {
  return vi
    .mocked(apiFetch)
    .mock.calls.filter(([, init]) => init?.method === method)
    .map(([url]) => url)
}

function renderPalette() {
  const opens: boolean[] = []
  vi.mocked(apiFetch).mockImplementation((input: string) =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve(payloadFor(input)),
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

function paletteInput(): HTMLInputElement {
  return document.body.querySelector(
    '[data-slot="command-input"]',
  ) as HTMLInputElement
}

function pressKey(key: string): KeyboardEvent {
  const input = paletteInput()
  const event = new KeyboardEvent('keydown', {
    key,
    bubbles: true,
    cancelable: true,
  })
  act(() => {
    input.dispatchEvent(event)
  })
  return event
}

function paletteRow(value: string): HTMLElement | null {
  return document.body.querySelector<HTMLElement>(
    `[cmdk-item=""][data-value="${value}"]`,
  )
}

function typeQuery(text: string) {
  const input = paletteInput()
  const setValue = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )?.set as (this: HTMLInputElement, value: string) => void
  act(() => {
    setValue.call(input, text)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

it('lists the active repo’s matching runs and lands on the run detail', async () => {
  const opens = renderPalette()
  typeQuery('palette')
  await act(async () => {})

  const row = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="run:COD-1342"]',
  )
  expect(row?.textContent).toContain('COD-1342')
  expect(row?.textContent).toContain('Runs search in the palette')
  expect(row?.textContent).toContain('pr')
  expect(row?.textContent).toContain('1h 30m')
  expect(
    document.body.querySelector('[cmdk-item=""][data-value="run:COD-31"]'),
  ).toBeNull()

  act(() => row?.click())

  expect(navigations).toEqual([
    { to: '/runs/$repo/$ticket', params: { repo: 'loop', ticket: 'COD-1342' } },
  ])
  expect(opens).toEqual([false])
})

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

// The cross-repo query is debounced, so rows only arrive once the timer has run.
async function settleSearch() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 200))
  })
  await act(async () => {})
}

it('surfaces every repo’s hits under All projects without flipping scope', async () => {
  active.isAll = true
  const opens = renderPalette()
  typeQuery('palette')
  await settleSearch()

  const issue = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="issue:loop:COD-1343"]',
  )
  expect(issue?.textContent).toContain('Cross-repo palette search')
  expect(issue?.textContent).toContain('loop')
  const run = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="run:atlas:COD-31"]',
  )
  expect(run?.textContent).toContain('Costs page')
  expect(run?.textContent).toContain('atlas')
  expect(
    document.body.querySelector(
      '[cmdk-item=""][data-value="setting:atlas:LINEAR_API_KEY"]',
    ),
  ).not.toBeNull()

  act(() => run?.click())

  expect(navigations).toEqual([
    { to: '/runs/$repo/$ticket', params: { repo: 'atlas', ticket: 'COD-31' } },
  ])
  expect(active.picked).toEqual([])
  expect(opens).toEqual([false])
})

it('starts the active repo’s loop from the Actions group', async () => {
  const opens = renderPalette()
  typeQuery('start loop')
  await act(async () => {})

  const row = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="action:start-loop"]',
  )
  expect(row?.textContent).toContain('Start loop')

  act(() => row?.click())
  await act(async () => {})

  expect(requestedUrls()).toContain('/api/v1/repos/loop/queue/drain')
  expect(opens).toEqual([false])
})

// ADR 0015 deleted the Run once page, so the action lands on the Loop card that
// took over picking the ticket and launching it.
it('lands the Run next action on the Loop card', async () => {
  const opens = renderPalette()
  typeQuery('run once')
  await act(async () => {})

  const row = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="action:run-next"]',
  )
  expect(row?.textContent).toContain('Run next')

  act(() => row?.click())

  expect(navigations).toEqual([{ to: '/loop' }])
  expect(opens).toEqual([false])
})

it('offers Stop instead of Start while the queue drains', async () => {
  hub.draining = true
  renderPalette()
  typeQuery('loop')

  // Offering Start for a repo that turns out to be draining is wrong for as long
  // as the queue takes to answer, so the row waits for it.
  expect(
    document.body.querySelector('[cmdk-item=""][data-value="action:start-loop"]'),
  ).toBeNull()

  await act(async () => {})

  expect(
    document.body.querySelector('[cmdk-item=""][data-value="action:stop-loop"]'),
  ).not.toBeNull()
  expect(
    document.body.querySelector('[cmdk-item=""][data-value="action:start-loop"]'),
  ).toBeNull()
})

it('hands a repo-scoped action to the switcher under All projects', async () => {
  active.isAll = true
  const opens = renderPalette()
  typeQuery('sync backlog')
  await settleSearch()

  const row = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="action:sync-backlog"]',
  )
  expect(row?.textContent).toContain('Sync backlog')

  act(() => row?.click())
  await act(async () => {})

  expect(active.switcher).toBe(1)
  expect(requestedUrls()).not.toContain('/api/v1/repos/loop/sync')
  expect(opens).toEqual([false])
})

it('lands the update check on the hub page', async () => {
  const opens = renderPalette()
  typeQuery('check for updates')
  await settleSearch()

  act(() => paletteRow('action:check-updates')?.click())
  await act(async () => {})

  expect(sentWith('POST')).toEqual(['/api/v1/update/check'])
  expect(navigations).toEqual([{ to: '/hub', hash: 'updates' }])
  expect(opens).toEqual([false])
})

it('switches to a cross-repo setting’s repo before landing on it', async () => {
  active.isAll = true
  renderPalette()
  typeQuery('linear')
  await settleSearch()

  const row = document.body.querySelector<HTMLElement>(
    '[cmdk-item=""][data-value="setting:atlas:LINEAR_API_KEY"]',
  )
  expect(row?.textContent).toContain('••••••••')

  act(() => row?.click())

  expect(active.picked).toEqual(['atlas'])
  expect(navigations).toEqual([
    { to: '/settings', search: { q: 'LINEAR_API_KEY' } },
  ])
})

it('scopes to the repo whose row was picked, not its same-named sibling', async () => {
  active.repos = repos
  const opens = renderPalette()
  await act(async () => {})

  act(() => paletteRow('/private/tmp/b')?.click())

  expect(active.picked).toEqual(['/private/tmp/b'])
  expect(opens).toEqual([false])
})

function paletteHighlight(): string | null {
  return (
    document.body
      .querySelector('[cmdk-item=""][aria-selected="true"]')
      ?.getAttribute('data-value') ?? null
  )
}

function breadcrumb(): string {
  return (
    document.body.querySelector('[data-slot="command-input-wrapper"]')
      ?.textContent ?? ''
  )
}

it('queues a ticket from its actions submenu, keyboard only', async () => {
  const opens = renderPalette()
  typeQuery('palette')
  await settleSearch()

  // Issues sits below Runs, so the highlight starts on the matching run.
  expect(paletteHighlight()).toBe('run:COD-1342')
  pressKey('ArrowDown')
  pressKey('Tab')

  expect(breadcrumb()).toContain('COD-1345')
  expect(paletteRow('issue:COD-1345')).toBeNull()
  expect(paletteHighlight()).toBe('issue-action:open')

  pressKey('ArrowDown')
  expect(paletteHighlight()).toBe('issue-action:queue')

  pressKey('Enter')
  await act(async () => {})

  expect(sentWith('POST')).toEqual(['/api/v1/repos/loop/queue'])
  expect(navigations).toEqual([])
  expect(opens).toEqual([false])
})

// The hint on the highlighted row is the mouse path into the same submenu, and
// it must not fire the row's own navigation on the way in.
it('archives a cross-repo ticket in its own repo from the row hint', async () => {
  active.isAll = true
  const opens = renderPalette()
  typeQuery('palette')
  await settleSearch()

  const hint = document.body.querySelector<HTMLElement>(
    '[aria-label="Actions for COD-1343"]',
  )
  act(() => hint?.click())

  expect(navigations).toEqual([])
  expect(breadcrumb()).toContain('COD-1343')

  act(() => paletteRow('issue-action:archive')?.click())
  await act(async () => {})

  expect(sentWith('PUT')).toEqual([
    '/api/v1/repos/loop/issues/COD-1343/archive',
  ])
  expect(opens).toEqual([false])
})

it('steps back from the submenu to the results without closing the palette', async () => {
  const opens = renderPalette()
  typeQuery('palette')
  await settleSearch()

  pressKey('ArrowDown')
  pressKey('Tab')
  expect(paletteRow('issue-action:open')).not.toBeNull()

  pressKey('Escape')

  expect(paletteRow('issue-action:open')).toBeNull()
  expect(paletteRow('issue:COD-1345')).not.toBeNull()
  expect(breadcrumb()).not.toContain('COD-1345')
  expect(opens).toEqual([])

  pressKey('Tab')
  const back = pressKey('Backspace')

  expect(paletteRow('issue-action:open')).toBeNull()
  expect(paletteRow('issue:COD-1345')).not.toBeNull()
  expect(opens).toEqual([])
  // happy-dom runs no default action, so the restored query surviving here only
  // proves the step back; the browser erasing a character from it does not.
  expect(back.defaultPrevented).toBe(true)
  expect(paletteInput().value).toBe('palette')
})

function groupHeadings(): string[] {
  return Array.from(document.body.querySelectorAll('[cmdk-group-heading]')).map(
    (el) => el.textContent ?? '',
  )
}

// A separator only earns its place between two groups, so the list may never end on one.
function endsOnSeparator(): boolean {
  const sections = document.body.querySelectorAll(
    '[cmdk-group],[cmdk-separator]',
  )
  const last = sections[sections.length - 1]
  return last?.hasAttribute('cmdk-separator') ?? false
}

it('keeps the unbounded Issues group below every bounded group', async () => {
  active.repos = repos
  renderPalette()
  typeQuery('loop')
  await settleSearch()

  expect(groupHeadings()).toEqual([
    'Actions',
    'Projects',
    'Navigation',
    'Settings',
    'Runs',
    'Issues',
  ])
  expect(endsOnSeparator()).toBe(false)
})

it('keeps the same group order under All projects', async () => {
  active.isAll = true
  active.repos = repos
  renderPalette()
  typeQuery('loop')
  await settleSearch()

  expect(groupHeadings()).toEqual([
    'Actions',
    'Projects',
    'Navigation',
    'Settings',
    'Runs',
    'Issues',
  ])
  expect(endsOnSeparator()).toBe(false)
})

it('leaves the empty-query layout alone', async () => {
  active.repos = repos
  renderPalette()
  await settleSearch()

  expect(groupHeadings()).toEqual([
    'Suggested',
    'Actions',
    'Projects',
    'Navigation',
  ])
  expect(endsOnSeparator()).toBe(false)
})

it('highlights the first row when issues are not the only hits', async () => {
  const opens = renderPalette()
  typeQuery('settings')
  await settleSearch()

  expect(groupHeadings()).toEqual(['Navigation', 'Issues'])
  expect(paletteHighlight()).toBe('Settings')

  pressKey('Enter')

  expect(navigations).toEqual([{ to: '/settings' }])
  expect(opens).toEqual([false])
})

it('highlights the top issue hit when only issues match', async () => {
  const opens = renderPalette()
  typeQuery('submenu')
  await settleSearch()

  expect(groupHeadings()).toEqual(['Issues'])
  expect(paletteHighlight()).toBe('issue:COD-1345')

  pressKey('Enter')

  expect(navigations).toEqual([
    { to: '/runs/$repo/$ticket', params: { repo: 'loop', ticket: 'COD-1345' } },
  ])
  expect(opens).toEqual([false])
})

it('holds the highlight while late issue rows append below', async () => {
  active.repos = repos
  renderPalette()
  typeQuery('loop')
  await act(async () => {})

  pressKey('ArrowDown')
  const moved = paletteHighlight()
  expect(moved).not.toBe('action:start-loop')

  await settleSearch()

  expect(paletteRow('issue:COD-1345')).not.toBeNull()
  expect(paletteHighlight()).toBe(moved)
})
