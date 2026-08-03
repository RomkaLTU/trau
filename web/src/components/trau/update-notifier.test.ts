// @vitest-environment happy-dom
import {
  notifyManager,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { act, createElement, Fragment } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { Toaster } from 'sonner'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { UpdateNotifier } from '@/components/trau/update-notifier'
import type { UpdateStatus } from '@/lib/update'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

notifyManager.setScheduler((cb) => cb())

let root: Root | undefined

function status(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    running: '2.1.0',
    onDisk: '2.1.0',
    latest: '2.2.0',
    latestNotes: '## Highlights\n\n- the queue drains politely now',
    restartPending: false,
    updateAvailable: true,
    installMethod: 'brew',
    upgradeCommand: 'brew upgrade --cask trau',
    checkedAt: null,
    checksEnabled: true,
    releaseUrl: 'https://github.com/RomkaLTU/trau/releases/tag/v2.2.0',
    applyState: { state: 'idle', message: '' },
    selfReloadPending: '',
    channel: 'release',
    channelRepo: '',
    channelRepos: [],
    channelSwitch: { state: 'idle', repoRoot: '', message: '' },
    releaseBinary: '',
    supervised: false,
    ...over,
  }
}

// The notifier reads /update through the shared query, so a test serves it the
// one status it wants and mounts the toaster its toast lands in. What has been
// announced outlives a mount the way it outlives a reload, so every test names a
// release of its own.
async function mount(served: UpdateStatus) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: () => Promise.resolve(served),
    } as Response),
  )
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  await act(async () => {
    mounted.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(
          Fragment,
          null,
          createElement(UpdateNotifier),
          createElement(Toaster),
        ),
      ),
    )
  })
  // The toaster paints a toast a tick after it is raised, and the status only
  // lands once the served fetch settles.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function unmount() {
  await act(async () => root?.unmount())
  document.body.innerHTML = ''
  root = undefined
}

function toasts(): string {
  return document.querySelector('[data-sonner-toaster]')?.textContent ?? ''
}

function button(label: string): HTMLButtonElement {
  const found = [...document.body.querySelectorAll('button')].find(
    (el) => el.textContent?.trim() === label,
  )
  if (!found) throw new Error(`no button labelled ${label}`)
  return found
}

afterEach(async () => {
  await unmount()
  vi.unstubAllGlobals()
})

describe('UpdateNotifier', () => {
  it('announces a release and opens its notes from the toast', async () => {
    await mount(status())

    expect(toasts()).toContain('trau v2.2.0 is available')

    await act(async () => button("What's new").click())

    expect(document.body.textContent).toContain("What's new in v2.2.0")
    expect(document.body.textContent).toContain('the queue drains politely now')
  })

  it('stays quiet about a release it already announced', async () => {
    await mount(status({ latest: '2.3.0' }))
    expect(toasts()).toContain('trau v2.3.0 is available')
    await unmount()

    await mount(status({ latest: '2.3.0' }))

    expect(toasts()).not.toContain('is available')
  })

  it('speaks up again once a newer release lands', async () => {
    await mount(status({ latest: '2.4.0' }))

    expect(toasts()).toContain('trau v2.4.0 is available')
  })

  it('says nothing when the hub has nothing newer', async () => {
    await mount(status({ latest: '2.5.0', updateAvailable: false }))

    expect(toasts()).not.toContain('is available')
  })
})
