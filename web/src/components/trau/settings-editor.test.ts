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
import type { ConfigKey } from '@/lib/config'

import { InlineEditor } from './settings-editor'

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

const REPO = 'demo'

const trackerKey: ConfigKey = {
  key: 'TRACKER_PROVIDER',
  value: 'linear',
  layer: 'project',
  group: 'Tracker',
  options: ['linear', 'jira', 'internal'],
  editable: true,
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  vi.mocked(apiFetch).mockResolvedValue({
    ok: true,
    json: async () => trackerKey,
  } as Response)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
  vi.mocked(apiFetch).mockReset()
})

function render(item: ConfigKey, syncedIssues: number) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['config', REPO], {
    repo: REPO,
    layers: ['project', 'user'],
    providers: [],
    keys: [item],
    synced_issues: syncedIssues,
  })
  act(() => {
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(InlineEditor, {
          repo: REPO,
          item,
          layers: ['project', 'user'],
          hubRestart: false,
          onCancel: () => {},
          onSaved: () => {},
        }),
      ),
    )
  })
}

function select(): HTMLSelectElement {
  const el = container.querySelector('select')
  if (!el) throw new Error('no select rendered')
  return el
}

function pick(value: string) {
  const el = select()
  act(() => {
    el.value = value
    el.dispatchEvent(new Event('change', { bubbles: true }))
  })
}

function button(label: string, scope: ParentNode = container): HTMLElement {
  const found = [...scope.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label,
  )
  if (!found) throw new Error(`no button labelled ${label}`)
  return found
}

function dialogButton(label: string): HTMLElement {
  const content = document.body.querySelector('[data-slot="alert-dialog-content"]')
  if (!content) throw new Error('no dialog open')
  return button(label, content)
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

function dialogText(): string | null {
  const title = document.body.querySelector('[data-slot="alert-dialog-title"]')
  if (!title) return null
  return document.body.querySelector('[data-slot="alert-dialog-content"]')
    ?.textContent ?? null
}

function writes() {
  return vi.mocked(apiFetch).mock.calls.filter(
    ([, init]) => (init as RequestInit | undefined)?.method === 'PUT',
  )
}

it('confirms the switch to internal while the repo still mirrors issues', async () => {
  render(trackerKey, 4)
  pick('internal')
  await click(button('Save'))

  expect(dialogText()).toContain('removes 4 synced issues')
  expect(dialogText()).toContain('History stays in linear')
  expect(writes()).toHaveLength(0)
})

it('counts one mirrored issue in the singular', async () => {
  render(trackerKey, 1)
  pick('internal')
  await click(button('Save'))

  expect(dialogText()).toContain('removes 1 synced issue from')
})

it('saves through the dialog once confirmed', async () => {
  render(trackerKey, 4)
  pick('internal')
  await click(button('Save'))
  await click(dialogButton('Switch'))

  expect(writes()).toHaveLength(1)
  expect(JSON.parse(String(writes()[0][1]?.body))).toMatchObject({
    key: 'TRACKER_PROVIDER',
    value: 'internal',
    layer: 'project',
  })
})

it('puts the select back when the switch is cancelled', async () => {
  render(trackerKey, 4)
  pick('internal')
  await click(button('Save'))
  await click(dialogButton('Cancel'))

  expect(dialogText()).toBeNull()
  expect(select().value).toBe('linear')
  expect(writes()).toHaveLength(0)
})

it('saves straight away when nothing is mirrored', async () => {
  render(trackerKey, 0)
  pick('internal')
  await click(button('Save'))

  expect(dialogText()).toBeNull()
  expect(writes()).toHaveLength(1)
})

it('saves straight away for any other tracker', async () => {
  render(trackerKey, 4)
  pick('jira')
  await click(button('Save'))

  expect(dialogText()).toBeNull()
  expect(writes()).toHaveLength(1)
})

it('leaves other keys unguarded', async () => {
  render(
    {
      key: 'VERIFY_EFFORT',
      value: 'low',
      layer: 'project',
      options: ['low', 'medium', 'high'],
      editable: true,
    },
    4,
  )
  pick('high')
  await click(button('Save'))

  expect(dialogText()).toBeNull()
  expect(writes()).toHaveLength(1)
})

it('names the queue entries the hub refused the switch over', async () => {
  vi.mocked(apiFetch).mockResolvedValue({
    ok: false,
    status: 409,
    json: async () => ({
      error: 'cannot switch to the internal tracker while the queue still holds tracker work',
      reason: 'queue_busy',
      blocked: [
        { repo: '/tmp/demo', id: 'COD-11', status: 'running' },
        { repo: '/tmp/demo', id: 'COD-12', status: 'pending' },
      ],
    }),
  } as Response)

  render(trackerKey, 4)
  pick('internal')
  await click(button('Save'))
  await click(dialogButton('Switch'))

  const blocked = container.querySelector('[data-slot="config-write-blocked"]')
  expect(blocked?.textContent).toContain('COD-11')
  expect(blocked?.textContent).toContain('running')
  expect(blocked?.textContent).toContain('COD-12')
  expect(blocked?.textContent).toContain('pending')
})
