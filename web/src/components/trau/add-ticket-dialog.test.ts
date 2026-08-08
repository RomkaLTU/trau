// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'

import { AddTicketDialog } from './add-ticket-dialog'

vi.mock('@/lib/api', () => ({ apiFetch: vi.fn() }))
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

let root: Root | undefined

function render() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => {
    mounted.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(AddTicketDialog, {
          repo: 'loop',
          queued: [],
          open: true,
          onOpenChange: () => {},
          onQueue: () => {},
        }),
      ),
    )
  })
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

it('lands focus in the search field instead of stranding it on the body', () => {
  render()

  const field = document.body.querySelector(
    'input[aria-label="Search tracker tickets"]',
  )
  expect(field).not.toBeNull()
  expect(document.activeElement).toBe(field)
})
