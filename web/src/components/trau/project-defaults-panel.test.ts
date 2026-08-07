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
import type { RepoView } from '@/lib/instances'
import type { ProjectTracker } from '@/lib/projects'

import { ProjectDefaultsSection } from './project-defaults-panel'

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))
vi.mock('@/components/trau/active-repo', () => ({
  useActiveRepo: () => ({ repos: REPOS }),
}))

;(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true

notifyManager.setScheduler((cb) => cb())

const PROJECT = 'p1'

function repo(name: string, root: string): RepoView {
  return {
    name,
    root,
    runs_dir: `${root}/.trau/runs`,
    live: false,
    allowed: true,
    registered: true,
    seeded: true,
    kind: 'repo',
  }
}

const REPOS = [repo('api', '/tmp/api'), repo('web', '/tmp/web')]

function tracker(
  provider: string,
  synced: Record<string, number>,
): ProjectTracker {
  return {
    project: PROJECT,
    repos: REPOS.map((r) => r.root),
    keys: [
      { key: 'TRACKER_PROVIDER', value: provider },
      { key: 'READY_LABEL', value: 'ready-for-agent' },
    ],
    synced_issues: synced,
  }
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  vi.mocked(apiFetch).mockResolvedValue({
    ok: true,
    json: async () => tracker('internal', { '/tmp/api': 0, '/tmp/web': 0 }),
  } as Response)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
  vi.mocked(apiFetch).mockReset()
})

function render(view: ProjectTracker) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['projects'], {
    projects: [
      { id: PROJECT, name: 'Platform', repos: REPOS.map((r) => r.root) },
    ],
  })
  client.setQueryData(['project-tracker', PROJECT], view)
  act(() => {
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(ProjectDefaultsSection, { repo: 'api' }),
      ),
    )
  })
}

function button(label: string, scope: ParentNode = container): HTMLElement {
  const found = [...scope.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label,
  )
  if (!found) throw new Error(`no button labelled ${label}`)
  return found
}

function switchButton(): HTMLElement | undefined {
  return [...container.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === 'Switch to internal tracker',
  )
}

function dialogText(): string | null {
  const content = document.body.querySelector(
    '[data-slot="alert-dialog-content"]',
  )
  return content?.textContent ?? null
}

function dialogButton(label: string): HTMLElement {
  const content = document.body.querySelector(
    '[data-slot="alert-dialog-content"]',
  )
  if (!content) throw new Error('no dialog open')
  return button(label, content)
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

function writes() {
  return vi
    .mocked(apiFetch)
    .mock.calls.filter(
      ([, init]) => (init as RequestInit | undefined)?.method === 'PUT',
    )
}

it('offers the switch while the project is on an external tracker', () => {
  render(tracker('jira', { '/tmp/api': 3, '/tmp/web': 1 }))

  expect(switchButton()).toBeDefined()
})

it('hides the switch once the project is internal', () => {
  render(tracker('internal', { '/tmp/api': 0, '/tmp/web': 0 }))

  expect(switchButton()).toBeUndefined()
  expect(
    container.querySelector('[data-slot="project-tracker-internal"]')
      ?.textContent,
  ).toContain('internal tracker')
})

it('hides the switch when the project has no tracker of its own', () => {
  render({ project: PROJECT, repos: [], keys: [], synced_issues: {} })

  expect(switchButton()).toBeUndefined()
})

it('names every member repo and what it would lose', async () => {
  render(tracker('jira', { '/tmp/api': 3, '/tmp/web': 1 }))
  await click(button('Switch to internal tracker'))

  const text = dialogText()
  expect(text).toContain('removes 4 synced issues')
  expect(text).toContain('History stays in jira')
  expect(text).toContain('api')
  expect(text).toContain('3 issues')
  expect(text).toContain('web')
  expect(text).toContain('1 issue')
  expect(writes()).toHaveLength(0)
})

it('says plainly when the project mirrors nothing', async () => {
  render(tracker('jira', { '/tmp/api': 0, '/tmp/web': 0 }))
  await click(button('Switch to internal tracker'))

  expect(dialogText()).toContain('No issues are mirrored from jira')
})

it('puts the whole project on the internal tracker once confirmed', async () => {
  render(tracker('jira', { '/tmp/api': 3, '/tmp/web': 1 }))
  await click(button('Switch to internal tracker'))
  await click(dialogButton('Switch'))

  expect(writes()).toHaveLength(1)
  expect(String(writes()[0][0])).toContain(`/projects/${PROJECT}/tracker`)
  expect(JSON.parse(String(writes()[0][1]?.body))).toEqual({
    keys: { TRACKER_PROVIDER: 'internal' },
  })
})

it('writes nothing when the switch is cancelled', async () => {
  render(tracker('jira', { '/tmp/api': 3, '/tmp/web': 1 }))
  await click(button('Switch to internal tracker'))
  await click(dialogButton('Cancel'))

  expect(dialogText()).toBeNull()
  expect(writes()).toHaveLength(0)
})

it('names the queue entries the hub refused the switch over', async () => {
  vi.mocked(apiFetch).mockResolvedValue({
    ok: false,
    status: 409,
    json: async () => ({
      error:
        'cannot switch to the internal tracker while the queue still holds tracker work',
      reason: 'queue_busy',
      blocked: [{ repo: '/tmp/web', id: 'COD-11', status: 'running' }],
    }),
  } as Response)

  render(tracker('jira', { '/tmp/api': 3, '/tmp/web': 1 }))
  await click(button('Switch to internal tracker'))
  await click(dialogButton('Switch'))

  const blocked = container.querySelector('[data-slot="config-write-blocked"]')
  expect(blocked?.textContent).toContain('COD-11')
  expect(blocked?.textContent).toContain('running')
  expect(blocked?.textContent).toContain('web')
  expect(blocked?.textContent).toContain('settle or remove it first')
})
