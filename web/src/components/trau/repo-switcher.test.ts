// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { apiFetch } from '@/lib/api'
import { projectRecent, saveRecents } from '@/lib/recents'

import { RepoSwitcherDialog } from './repo-switcher'

const { repos, picked } = vi.hoisted(() => ({
  repos: [
    '/Users/rd/Projects/loop',
    '/Users/rd/Projects/qa-1/shipflock',
    '/Users/rd/Projects/qa-2/shipflock',
  ].map((root) => ({
    name: root.split('/').pop() as string,
    root,
    runs_dir: `${root}/runs`,
    live: false,
    allowed: true,
    registered: true,
    seeded: false,
    kind: 'repo' as const,
  })),
  picked: [] as string[],
}))

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => () => {} }))
vi.mock('@/components/trau/active-repo', () => ({
  ALL_SCOPE: 'all',
  useActiveRepo: () => ({
    repo: 'loop',
    root: '/Users/rd/Projects/loop',
    repos,
    isAll: false,
    setScope: (next: string) => picked.push(next),
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

function serve(projects: { id: string; name: string; repos: string[] }[]) {
  vi.mocked(apiFetch).mockImplementation((input: string) =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve(
          input.includes('projects') ? { projects } : { instances: [], repos },
        ),
    } as Response),
  )
}

let root: Root | undefined

async function open() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  await act(async () => {
    mounted.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(RepoSwitcherDialog, { open: true, onOpenChange: () => {} }),
      ),
    )
  })
}

function items(): HTMLElement[] {
  return [...document.body.querySelectorAll<HTMLElement>('[cmdk-item=""]')]
}

function values(prefix: string): string[] {
  return items()
    .map((el) => el.getAttribute('data-value') ?? '')
    .filter((value) => value.startsWith(prefix))
}

function selectedValue(): string | null {
  const selected = items().filter(
    (el) => el.getAttribute('aria-selected') === 'true',
  )
  expect(selected).toHaveLength(1)
  return selected[0].getAttribute('data-value')
}

function input(): HTMLInputElement {
  return document.body.querySelector('input') as HTMLInputElement
}

function arrowDown() {
  act(() => {
    input().dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }),
    )
  })
}

function type(query: string) {
  const setValue = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )?.set
  act(() => {
    setValue?.call(input(), query)
    input().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

beforeEach(() => serve([]))

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  picked.length = 0
  localStorage.clear()
  vi.mocked(apiFetch).mockReset()
})

it('keys same-named repos apart by root', async () => {
  await open()

  expect(values('repo:')).toEqual([
    'repo:/Users/rd/Projects/loop',
    'repo:/Users/rd/Projects/qa-1/shipflock',
    'repo:/Users/rd/Projects/qa-2/shipflock',
  ])
})

it('steps the highlight through same-named repos one row at a time', async () => {
  await open()

  arrowDown()
  expect(selectedValue()).toBe('repo:/Users/rd/Projects/loop')
  arrowDown()
  expect(selectedValue()).toBe('repo:/Users/rd/Projects/qa-1/shipflock')
  arrowDown()
  expect(selectedValue()).toBe('repo:/Users/rd/Projects/qa-2/shipflock')
})

it('picks the repo the highlight sits on', async () => {
  await open()

  arrowDown()
  arrowDown()
  expect(selectedValue()).toBe('repo:/Users/rd/Projects/qa-1/shipflock')
  act(() => {
    input().dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
    )
  })

  expect(picked).toEqual(['/Users/rd/Projects/qa-1/shipflock'])
})

it('tells same-named rows apart by root when no project does', async () => {
  await open()

  expect(items().map((el) => el.textContent)).toEqual(
    expect.arrayContaining([
      expect.stringContaining('shipflock · …/qa-1/shipflock'),
      expect.stringContaining('shipflock · …/qa-2/shipflock'),
    ]),
  )
})

it('tells same-named rows apart by the project holding each', async () => {
  serve([
    { id: 'qa-1', name: 'QA one', repos: ['/Users/rd/Projects/qa-1/shipflock'] },
    { id: 'qa-2', name: 'QA two', repos: ['/Users/rd/Projects/qa-2/shipflock'] },
  ])
  await open()

  expect(items().map((el) => el.textContent)).toEqual(
    expect.arrayContaining([
      expect.stringContaining('shipflock · QA one'),
      expect.stringContaining('shipflock · QA two'),
    ]),
  )
})

it('leaves a name only one repo carries unqualified', async () => {
  await open()

  const loop = items().find(
    (el) => el.getAttribute('data-value') === 'repo:/Users/rd/Projects/loop',
  )
  expect(loop?.textContent).toContain('loop')
  expect(loop?.textContent).not.toContain('·')
})

it('keys the repos under a project row by root too', async () => {
  serve([
    {
      id: 'qa',
      name: 'QA',
      repos: [
        '/Users/rd/Projects/qa-1/shipflock',
        '/Users/rd/Projects/qa-2/shipflock',
      ],
    },
  ])
  await open()

  type('shipflock')
  expect(values('repo:')).toEqual([
    'repo:/Users/rd/Projects/qa-1/shipflock',
    'repo:/Users/rd/Projects/qa-2/shipflock',
  ])
})

it('recalls the repo whose root the recent recorded', async () => {
  saveRecents([projectRecent(repos[1], 1)])
  await open()

  expect(values('recent:')).toEqual(['recent:/Users/rd/Projects/qa-1/shipflock'])
})

it('filters on the repo name, not the root the row is keyed by', async () => {
  await open()

  type('shipflock')
  expect(values('repo:')).toEqual([
    'repo:/Users/rd/Projects/qa-1/shipflock',
    'repo:/Users/rd/Projects/qa-2/shipflock',
  ])
})
