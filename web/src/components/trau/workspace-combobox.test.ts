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

import { WorkspaceCombobox } from './workspace-combobox'

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}

const workspaces = [
  { name: 'storefront', path: 'apps/storefront', dir_name: 'storefront' },
  { name: 'admin', path: 'apps/admin', dir_name: 'admin' },
  { name: '', path: 'packages/ui', dir_name: 'ui' },
]

let root: Root | undefined

async function render() {
  vi.mocked(apiFetch).mockResolvedValue({
    ok: true,
    status: 200,
    json: () => Promise.resolve({ workspaces }),
  } as Response)
  const chosen: string[] = []
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
        createElement(WorkspaceCombobox, {
          id: 'ws',
          repo: 'loop',
          value: '',
          label: 'App URL workspace',
          onChange: (next: string) => chosen.push(next),
        }),
      ),
    )
  })
  return chosen
}

function input(): HTMLInputElement {
  return document.body.querySelector('input') as HTMLInputElement
}

function options(): HTMLElement[] {
  return [...document.body.querySelectorAll<HTMLElement>('[role="option"]')]
}

function activeOption(): HTMLElement | null {
  const id = input().getAttribute('aria-activedescendant')
  return id ? document.getElementById(id) : null
}

function focus() {
  act(() => {
    input().focus()
    input().dispatchEvent(new FocusEvent('focus', { bubbles: false }))
  })
}

function key(name: string) {
  act(() => {
    input().dispatchEvent(
      new KeyboardEvent('keydown', { key: name, bubbles: true }),
    )
  })
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(apiFetch).mockReset()
})

describe('WorkspaceCombobox', () => {
  it('names the highlighted option with aria-activedescendant', async () => {
    await render()
    focus()

    expect(input().getAttribute('aria-expanded')).toBe('true')
    expect(activeOption()?.textContent).toContain('storefront')

    key('ArrowDown')
    expect(activeOption()?.textContent).toContain('admin')
    expect(activeOption()?.getAttribute('aria-selected')).toBe('true')
  })

  it('jumps to the ends with Home and End', async () => {
    await render()
    focus()

    key('End')
    expect(activeOption()?.textContent).toContain('packages/ui')
    key('Home')
    expect(activeOption()?.textContent).toContain('storefront')
  })

  it('commits the highlighted suggestion on Enter', async () => {
    const chosen = await render()
    focus()

    key('ArrowDown')
    key('Enter')

    expect(chosen).toEqual(['admin'])
    expect(options()).toHaveLength(0)
  })

  it('closes on Escape and keeps focus on the field', async () => {
    await render()
    focus()

    expect(options()).toHaveLength(3)
    key('Escape')

    expect(options()).toHaveLength(0)
    expect(input().getAttribute('aria-expanded')).toBe('false')
    expect(input().getAttribute('aria-activedescendant')).toBeNull()
    expect(document.activeElement).toBe(input())
  })
})
