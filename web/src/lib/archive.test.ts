// @vitest-environment happy-dom
import { notifyManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiFetch } from './api'
import { archiveToastMessage, useArchiveIssue } from './archive'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

const mockFetch = vi.mocked(apiFetch)
let root: Root | undefined

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  mockFetch.mockReset()
})

describe('archiveToastMessage', () => {
  it('names the archived issue', () => {
    expect(archiveToastMessage('COD-123', true, 0)).toBe('Archived COD-123')
  })

  it('reports pruned queued items, pluralized', () => {
    expect(archiveToastMessage('COD-123', true, 2)).toBe(
      'Archived COD-123 — removed 2 queued items',
    )
    expect(archiveToastMessage('COD-123', true, 1)).toBe(
      'Archived COD-123 — removed 1 queued item',
    )
  })

  it('names the restored issue on an unarchive and never mentions the queue', () => {
    expect(archiveToastMessage('COD-123', false, 0)).toBe('Unarchived COD-123')
  })
})

describe('useArchiveIssue', () => {
  // Two archives in one tick is what a held 'a' delivers: the second keystroke
  // arrives before the render that would have reported the first as pending.
  it('sends one request when the archive is asked for twice before a render', async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ id: 'COD-123', queue_removed: 0 }),
    } as Response)

    function Probe() {
      const archive = useArchiveIssue('acme', () => {})
      return createElement(
        'button',
        {
          onClick: () => {
            archive.mutate({ id: 'COD-123', archived: true })
            archive.mutate({ id: 'COD-123', archived: true })
          },
        },
        'archive',
      )
    }

    const host = document.createElement('div')
    document.body.appendChild(host)
    const mounted = createRoot(host)
    root = mounted
    act(() =>
      mounted.render(
        createElement(
          QueryClientProvider,
          { client: new QueryClient() },
          createElement(Probe),
        ),
      ),
    )
    await act(async () => {
      host.querySelector('button')!.click()
    })

    expect(mockFetch).toHaveBeenCalledTimes(1)
  })
})
