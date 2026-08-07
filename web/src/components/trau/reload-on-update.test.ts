// @vitest-environment happy-dom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi, type Mock } from 'vitest'

import { ReloadOnUpdate } from '@/components/trau/reload-on-update'
import { resetHubWatch } from '@/lib/connectivity'

let container: HTMLDivElement
let root: Root
let client: QueryClient
let reload: Mock<() => void>
let served: string | undefined
let reachable: boolean
let uptime: number

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

function stubHealth() {
  vi.stubGlobal('fetch', async () => {
    if (!reachable) throw new TypeError('failed to fetch')
    uptime += 5
    return new Response(
      JSON.stringify({
        status: 'ok',
        version: '2.21.0',
        ...(served === undefined ? {} : { webBuild: served }),
        uptime_seconds: uptime,
        token_required: false,
      }),
      { status: 200, headers: { 'content-type': 'application/json' } },
    )
  })
}

async function mount() {
  await act(async () =>
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(ReloadOnUpdate),
      ),
    ),
  )
  await settle()
}

// The first poll resolves a tick after the render, so nothing the mount decides
// is observable until the queue drains.
async function settle() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function poll() {
  await act(async () => {
    await client.refetchQueries({ queryKey: ['health'] })
  })
}

function notice(): HTMLElement | null {
  return container.querySelector('[role="status"]')
}

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  reload = vi.fn()
  vi.stubGlobal('location', { ...globalThis.location, reload })
  served = 'gserver1'
  reachable = true
  uptime = 0
  sessionStorage.clear()
  stubHealth()
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
  resetHubWatch()
})

// __WEB_BUILD__ is baked in by vite's define, so the page's own stamp is
// whatever this checkout is at — the fixture only has to differ from it.
it('reloads once when the hub serves a different build', async () => {
  await mount()

  expect(reload).toHaveBeenCalledTimes(1)
  expect(sessionStorage.getItem('trau.web-build.reloaded')).toBe('gserver1')
  expect(notice()).toBeNull()
})

it('does not reload again for the same stamp, and says so instead', async () => {
  sessionStorage.setItem('trau.web-build.reloaded', 'gserver1')

  await mount()

  expect(reload).not.toHaveBeenCalled()
  expect(notice()?.textContent).toContain('trau was updated')
})

it('reloads for a stamp the window has not spent its reload on', async () => {
  sessionStorage.setItem('trau.web-build.reloaded', 'golder0')

  await mount()

  expect(reload).toHaveBeenCalledTimes(1)
  expect(sessionStorage.getItem('trau.web-build.reloaded')).toBe('gserver1')
})

it('stays quiet when the page already runs the served build', async () => {
  served = __WEB_BUILD__

  await mount()

  expect(reload).not.toHaveBeenCalled()
  expect(notice()).toBeNull()
})

it('never reloads against a hub that answers no build stamp', async () => {
  served = undefined

  await mount()
  await poll()

  expect(reload).not.toHaveBeenCalled()
  expect(notice()).toBeNull()
})

it('does nothing while the hub is down', async () => {
  reachable = false

  await mount()
  await poll()

  expect(reload).not.toHaveBeenCalled()
  expect(notice()).toBeNull()
})

it('waits for an entry in progress and reloads on the next poll', async () => {
  const answer = document.createElement('textarea')
  document.body.appendChild(answer)
  answer.value = 'half of an interview answer'
  answer.focus()

  await mount()
  expect(reload).not.toHaveBeenCalled()

  await poll()
  expect(reload).not.toHaveBeenCalled()

  answer.blur()
  await poll()
  expect(reload).toHaveBeenCalledTimes(1)

  answer.remove()
})
