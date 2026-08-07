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

async function render(): Promise<string> {
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
          keys,
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
    type(input('New board column name'), 'Offline Column')
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
