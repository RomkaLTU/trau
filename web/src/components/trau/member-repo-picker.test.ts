// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiFetch } from '@/lib/api'

import { StartIn } from './member-repo-picker'

const { members } = vi.hoisted(() => ({
  members: ['api', 'image-service', 'web'].map((name) => ({
    name,
    root: `/repos/${name}`,
    runs_dir: `/repos/${name}/runs`,
    live: false,
    allowed: true,
    registered: true,
    seeded: false,
  })),
}))

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))
vi.mock('@/components/trau/active-repo', () => ({
  useActiveRepo: () => ({ repos: members }),
}))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

const mockFetch = vi.mocked(apiFetch)

// serve answers the projects list with one three-member project, and the hint
// with the members that project's ticket can actually start in.
function serve(startable: string[], suggested: string) {
  mockFetch.mockImplementation((input: string) =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve(
          input.includes('start-repo')
            ? {
                project: 'platform',
                repos: startable.map((name) => ({
                  name,
                  root: `/repos/${name}`,
                })),
                suggested,
                reason: 'first',
              }
            : {
                projects: [
                  {
                    id: 'platform',
                    name: 'Platform',
                    repos: members.map((member) => member.root),
                  },
                ],
              },
        ),
    } as Response),
  )
}

let root: Root | undefined

function renderStartIn() {
  const started: string[] = []
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  function Probe() {
    return createElement(StartIn, {
      repo: 'api',
      ticket: 'API-2',
      onStart: (member: string) => started.push(member),
      children: (begin: () => void) =>
        createElement('button', { onClick: begin }, 'Add to queue'),
    })
  }
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => {
    mounted.render(
      createElement(QueryClientProvider, { client }, createElement(Probe)),
    )
  })
  return started
}

function click(label: string) {
  const button = [...document.body.querySelectorAll('button')].find((b) =>
    b.textContent?.trim().startsWith(label),
  )
  if (!button) throw new Error(`no button labelled ${label}`)
  act(() => button.click())
}

function listbox() {
  return document.body.querySelector('[role="listbox"]')
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  mockFetch.mockReset()
})

describe('StartIn', () => {
  it('starts on the first click when the ticket lives in one member', async () => {
    serve(['api'], 'api')
    const started = renderStartIn()
    await act(async () => {})

    click('Add to queue')
    await act(async () => {})

    expect(started).toEqual(['api'])
    expect(listbox()).toBeNull()
  })

  it('asks which member when the ticket can start in several', async () => {
    serve(['api', 'image-service', 'web'], 'image-service')
    const started = renderStartIn()
    await act(async () => {})

    click('Add to queue')
    await act(async () => {})

    expect(started).toEqual([])
    expect(listbox()).not.toBeNull()

    click('image-service')
    expect(started).toEqual(['image-service'])
  })
})
