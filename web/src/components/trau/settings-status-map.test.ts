// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { TrackerAdvanced } from '@/components/trau/settings-status-map'
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
  { key: 'READY_LABEL', value: 'ready', layer: 'default', editable: true },
  { key: 'AZURE_BOARD_STATES', value: '', layer: 'default', editable: true },
  { key: 'STATUS_TODO', value: '', layer: 'default', editable: true },
  { key: 'STATUS_IN_PROGRESS', value: '', layer: 'default', editable: true },
  { key: 'STATUS_IN_REVIEW', value: '', layer: 'default', editable: true },
  { key: 'STATUS_DONE', value: '', layer: 'default', editable: true },
]

function stubFetch(status: number, body: unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { 'Content-Type': 'application/json' },
        }),
    ),
  )
}

// genericRows are the rows the editor did not take over — each one rendered by
// the caller's own row renderer, so their presence is the assertion that the
// generic list is untouched.
function genericRows(): string[] {
  return Array.from(container.querySelectorAll('[data-generic-row]')).map(
    (el) => el.textContent ?? '',
  )
}

function input(label: string): HTMLInputElement {
  return container.querySelector(`input[aria-label="${label}"]`)!
}

function button(text: string): HTMLButtonElement {
  return Array.from(container.querySelectorAll('button')).find((el) =>
    (el.textContent ?? '').includes(text),
  )!
}

function pin(name: string): HTMLButtonElement {
  return container.querySelector(`button[aria-label="Map ${name} by hand"]`)!
}

function select(label: string): HTMLElement {
  return container.querySelector(`[aria-label="${label}"]`)!
}

function layerButton(group: string, layer: string): HTMLButtonElement {
  const control = container.querySelector(`[aria-label="${group}"]`)!
  return Array.from(control.querySelectorAll('button')).find(
    (el) => el.textContent === layer,
  )!
}

// React listens for the native input event, so the value has to go through the
// DOM setter it does not track rather than through the React prop.
function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )!.set!
  setter.call(el, value)
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

async function render(which: ConfigKey[] = keys): Promise<string> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  await act(async () => {
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(TrackerAdvanced, {
          repo: 'acme',
          keys: which,
          layers: ['project', 'user'],
          hubRestart: false,
          onSaved: () => {},
          renderRow: (item: ConfigKey) =>
            createElement(
              'div',
              { key: item.key, 'data-generic-row': true },
              item.key,
            ),
        }),
      ),
    )
  })
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  return container.textContent ?? ''
}

it('renders every generic row unchanged on a provider with no mapping editor', async () => {
  stubFetch(404, { error: 'this repo has no status-mapping options' })

  const text = await render()
  expect(genericRows()).toEqual(keys.map((k) => k.key))
  expect(text).not.toContain('board column grouping')
})

// linearKeys is the same set a Linear repo carries: the overlay key in place of
// the Azure one, already holding a single override.
const linearKeys: ConfigKey[] = [
  { key: 'READY_LABEL', value: 'ready', layer: 'default', editable: true },
  {
    key: 'LINEAR_BOARD_STATES',
    value: 'Ready for QA=done',
    layer: 'repo',
    editable: true,
  },
  { key: 'STATUS_TODO', value: '', layer: 'default', editable: true },
  { key: 'STATUS_IN_PROGRESS', value: '', layer: 'default', editable: true },
  { key: 'STATUS_IN_REVIEW', value: '', layer: 'default', editable: true },
  { key: 'STATUS_DONE', value: '', layer: 'default', editable: true },
]

it('renders the editor above the generic rows, minus the five keys it owns', async () => {
  stubFetch(200, {
    provider: 'azure',
    grouping: [{ name: 'Ready to Develop', suggestedGroup: 'unstarted' }],
    pinOptions: [{ name: 'Active', category: 'InProgress' }],
  })

  const text = await render()
  expect(text).toContain('board column grouping')
  expect(text).toContain('Ready to Develop')
  expect(genericRows()).toEqual(['READY_LABEL'])
  // The selects prefill from the board's suggestions, but nothing is written
  // yet, so the preview reads the key as the empty value it still is.
  expect(text).toContain('(empty — grouping stays category-derived)')
})

it('degrades to the config-only editor when the board cannot be read', async () => {
  stubFetch(200, {
    provider: 'azure',
    grouping: [],
    pinOptions: [],
    error: 'azure: unauthorized',
    hint: 'Regenerate the personal access token.',
  })

  const text = await render()
  expect(text).toContain('azure: unauthorized')
  expect(text).toContain('Regenerate the personal access token.')
  expect(text).toContain('board column grouping')
  expect(genericRows()).toEqual(['READY_LABEL'])
})

it('keeps an uncommitted grouping edit when the shared write target moves', async () => {
  stubFetch(200, {
    provider: 'azure',
    grouping: [],
    pinOptions: [],
    error: 'azure: unauthorized',
    hint: 'Regenerate the personal access token.',
  })

  await render()
  await act(async () => {
    type(input('New column name'), 'Offline Column')
  })
  await act(async () => {
    button('Add column').click()
  })
  expect(container.textContent).toContain('Offline Column')

  await act(async () => {
    layerButton('AZURE_BOARD_STATES write target', 'user').click()
  })
  expect(container.textContent).toContain('Offline Column')
})

it('renders the overlay editor for a Linear repo, prefilled from the state types', async () => {
  stubFetch(200, {
    provider: 'linear',
    grouping: [
      { name: 'Triage', suggestedGroup: 'backlog' },
      { name: 'Ready for QA', suggestedGroup: 'started' },
    ],
    pinOptions: [{ name: 'Ready for QA', category: 'started' }],
  })

  const text = await render(linearKeys)
  expect(text).toContain('workflow state grouping')
  expect(text).toContain('LINEAR_BOARD_STATES')
  expect(genericRows()).toEqual(['READY_LABEL'])
  // Triage is unlisted, so it sits on the section its own state type gives it
  // rather than on Other, and the preview reads the one pair the key carries.
  expect(select('Triage group').textContent).toContain('Backlog')
  expect(select('Ready for QA group').textContent).toContain('Done')
  expect(select('Triage group').textContent).not.toContain('Other')
  expect(text).toContain('Ready for QA=done')
  expect(text).not.toContain('board column grouping')
})

it('keeps an uncommitted overlay edit when the shared write target moves', async () => {
  stubFetch(200, {
    provider: 'linear',
    grouping: [],
    pinOptions: [],
    error: 'linear: unauthorized',
    hint: 'Regenerate the Linear API key.',
  })

  await render(linearKeys)
  await act(async () => {
    type(input('New state name'), 'Archived')
  })
  await act(async () => {
    button('Add state').click()
  })
  expect(container.textContent).toContain('Archived')

  await act(async () => {
    layerButton('LINEAR_BOARD_STATES write target', 'user').click()
  })
  expect(container.textContent).toContain('Archived')
})

// jiraKeys is the same set a Jira repo carries: the Jira overlay key in place of
// the Azure one, already holding a single override.
const jiraKeys: ConfigKey[] = [
  { key: 'READY_LABEL', value: 'ready', layer: 'default', editable: true },
  {
    key: 'JIRA_BOARD_STATES',
    value: 'Closed=done',
    layer: 'repo',
    editable: true,
  },
  { key: 'STATUS_TODO', value: '', layer: 'default', editable: true },
  { key: 'STATUS_IN_PROGRESS', value: '', layer: 'default', editable: true },
  { key: 'STATUS_IN_REVIEW', value: '', layer: 'default', editable: true },
  { key: 'STATUS_DONE', value: '', layer: 'default', editable: true },
]

it('renders the overlay editor for a Jira repo, prefilled from the status categories', async () => {
  stubFetch(200, {
    provider: 'jira',
    grouping: [
      { name: 'To Do', suggestedGroup: 'unstarted' },
      { name: 'Closed', suggestedGroup: 'done' },
    ],
    pinOptions: [{ name: 'Closed', category: 'done' }],
  })

  const text = await render(jiraKeys)
  expect(text).toContain('workflow status grouping')
  expect(text).toContain('JIRA_BOARD_STATES')
  expect(genericRows()).toEqual(['READY_LABEL'])
  // To Do is unlisted, so it sits on the section its own status category gives
  // it rather than on Other, and the preview reads the one pair the key carries.
  expect(select('To Do group').textContent).toContain('Todo')
  expect(select('Closed group').textContent).toContain('Done')
  expect(select('To Do group').textContent).not.toContain('Other')
  // Closed is mapped to the very section its category derives, so the badge is
  // the only place that pair shows at all.
  expect(pin('Closed').getAttribute('aria-pressed')).toBe('true')
  expect(text).toContain('Closed=done')
  expect(text).not.toContain('board column grouping')
  expect(text).not.toContain('workflow state grouping')
})

it('pluralizes a Jira status list without spelling it statuss', async () => {
  stubFetch(200, {
    provider: 'jira',
    grouping: [],
    pinOptions: [],
    error: 'jira: unauthorized',
    hint: 'Regenerate the API token.',
  })

  const text = await render(
    jiraKeys.map((k) =>
      k.key === 'JIRA_BOARD_STATES' ? { ...k, value: '' } : k,
    ),
  )
  expect(text).toContain('no statuses yet')
  expect(text).not.toContain('statuss')
})

// A Jira status whose category already derives done still needs the mapping to
// hold every issue in it — an unmapped one resolved won't-do or duplicate groups
// as canceled. The section select cannot say that, so the badge is where the
// pair is made.
it('maps a Jira status to the section its category already derives', async () => {
  stubFetch(200, {
    provider: 'jira',
    grouping: [
      { name: 'To Do', suggestedGroup: 'unstarted' },
      { name: 'Closed', suggestedGroup: 'done' },
    ],
    pinOptions: [],
  })

  await render(
    jiraKeys.map((k) =>
      k.key === 'JIRA_BOARD_STATES' ? { ...k, value: '' } : k,
    ),
  )
  expect(pin('Closed').textContent).toBe('derived')
  // Only a section the category derives conditionally carries the choice.
  expect(
    container.querySelector('button[aria-label="Map To Do by hand"]'),
  ).toBeNull()

  await act(async () => {
    pin('Closed').click()
  })
  expect(pin('Closed').getAttribute('aria-pressed')).toBe('true')
  expect(container.textContent).toContain('will write: Closed=done')
})

it('keeps an uncommitted Jira overlay edit when the shared write target moves', async () => {
  stubFetch(200, {
    provider: 'jira',
    grouping: [],
    pinOptions: [],
    error: 'jira: unauthorized',
    hint: 'Regenerate the API token.',
  })

  await render(jiraKeys)
  await act(async () => {
    type(input('New status name'), 'Archived')
  })
  await act(async () => {
    button('Add status').click()
  })
  expect(container.textContent).toContain('Archived')

  await act(async () => {
    layerButton('JIRA_BOARD_STATES write target', 'user').click()
  })
  expect(container.textContent).toContain('Archived')
})
