// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { ConfigView } from '@/routes/settings'
import { ActiveRepoProvider } from '@/components/trau/active-repo'
import { deriveSections } from '@/lib/settings'
import type { ConfigKey } from '@/lib/config'

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
  vi.unstubAllGlobals()
})

const keys: ConfigKey[] = [
  {
    key: 'TRACKER_PROVIDER',
    value: 'linear',
    layer: 'default',
    group: 'Tracker & issues',
    options: ['linear', 'jira'],
    editable: true,
  },
  {
    key: 'JIRA_BASE_URL',
    value: '',
    layer: 'default',
    group: 'Tracker & issues',
    tracker: 'jira',
    editable: true,
  },
  {
    key: 'READY_LABEL',
    value: 'ready',
    layer: 'user',
    group: 'Tracker & issues',
    editable: true,
  },
  {
    key: 'MAX_REPAIRS',
    value: '4',
    layer: 'user',
    group: 'Pipeline behavior',
    kind: 'int',
    editable: true,
  },
]

const responses: Record<string, unknown> = {
  '/api/v1/config': { repo: '', layers: ['user'], providers: ['claude'], keys },
  '/api/v1/repos': {
    repos: [
      { name: 'acme', root: '/tmp/acme', live: true },
      { name: 'beta', root: '/tmp/beta', live: false },
    ],
  },
  '/api/v1/prompts': { prompts: [] },
  '/api/v1/projects': { projects: [] },
}

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string) => {
      const path = String(input).split('?')[0]
      const body = responses[path] ?? {}
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
}

async function renderGlobal() {
  stubFetch()
  await act(async () => {
    root.render(
      createElement(
        QueryClientProvider,
        {
          client: new QueryClient({
            defaultOptions: { queries: { retry: false } },
          }),
        },
        createElement(
          ActiveRepoProvider,
          null,
          createElement(ConfigView, { repo: null }),
        ),
      ),
    )
  })
  // The config query resolves over a few microtask turns; the skeleton is what
  // renders until then.
  for (let i = 0; i < 20 && text().includes('loading'); i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
  }
}

function text(): string {
  return container.textContent ?? ''
}

function button(label: string): HTMLButtonElement {
  return container.querySelector(`button[aria-label="${label}"]`)!
}

it('renders the global editor with its scope banner under All projects', async () => {
  await renderGlobal()

  expect(text()).toContain('Global defaults')
  expect(text()).toContain('~/.trau.ini')
  expect(text()).toContain('MAX_REPAIRS')
  expect(text()).toContain('Pipeline behavior')
})

it('renders Tracker & issues unfiltered — there is no active tracker', async () => {
  await renderGlobal()

  expect(text()).toContain('JIRA_BASE_URL')
  expect(text()).toContain('READY_LABEL')

  // The same keys under a project scope: the jira field is an inactive-tracker
  // key there, so only the global view shows it without a search.
  const scoped = deriveSections(keys, 'linear').find(
    (s) => s.group === 'Tracker & issues',
  )!
  expect(scoped.hiddenKeys.map((k) => k.key)).toContain('JIRA_BASE_URL')
})

it('leaves out the repo-scoped panels but keeps the global prompts card', async () => {
  await renderGlobal()

  expect(text()).toContain('Prompts')
  expect(text()).not.toContain('Repo prompts')
  expect(text()).not.toContain('Team sync status')
  expect(text()).not.toContain('Project defaults')
})

it('edits write to the user layer with no write-target choice', async () => {
  await renderGlobal()

  await act(async () => {
    button('Edit MAX_REPAIRS').click()
  })

  expect(container.querySelector('input[aria-label="MAX_REPAIRS value"]')).not
    .toBeNull()
  expect(text()).not.toContain('write to:')
  expect(text()).toContain('user layer applies to every repo on this machine')
})
