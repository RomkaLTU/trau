// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { BeforeInstallPromptEvent, PwaInstall } from './pwa-install'

type PwaInstallModule = typeof import('./pwa-install')

const DISMISSED_KEY = 'trau.pwa-install.dismissed'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

// Each test loads the module afresh: the captured offer and the cached mark are
// module state, and the listener is registered as the module loads.
async function loadPwaInstall(): Promise<PwaInstallModule> {
  vi.resetModules()
  return import('./pwa-install')
}

function fireInstallPrompt(outcome: 'accepted' | 'dismissed' = 'accepted') {
  const event = Object.assign(
    new Event('beforeinstallprompt', { cancelable: true }),
    {
      prompt: vi.fn(() => Promise.resolve()),
      userChoice: Promise.resolve({ outcome }),
    },
  ) satisfies BeforeInstallPromptEvent
  globalThis.dispatchEvent(event)
  return event
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  localStorage.clear()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

// mount renders the hook and reports what it last returned, so a store change
// is read the way the banner reads it.
function mount(usePwaInstall: () => PwaInstall): { current: PwaInstall } {
  const seen = { current: null as PwaInstall | null }
  function Probe() {
    seen.current = usePwaInstall()
    return null
  }
  act(() => root.render(createElement(Probe)))
  return seen as { current: PwaInstall }
}

describe('dismissal store', () => {
  it('reads no mark as never dismissed', async () => {
    const pwa = await loadPwaInstall()
    expect(pwa.loadDismissed()).toBe(false)
  })

  it('writes a mark that outlives the page load', async () => {
    const pwa = await loadPwaInstall()
    pwa.dismiss()
    expect(localStorage.getItem(DISMISSED_KEY)).toBe('1')
    expect((await loadPwaInstall()).loadDismissed()).toBe(true)
  })

  it('holds no mark where storage is missing', async () => {
    vi.stubGlobal('localStorage', undefined)
    const pwa = await loadPwaInstall()
    expect(pwa.loadDismissed()).toBe(false)
    expect(() => pwa.dismiss()).not.toThrow()
  })

  it('holds no mark where storage is blocked', async () => {
    vi.stubGlobal('localStorage', undefined)
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get() {
        throw new Error('storage is blocked')
      },
    })
    const pwa = await loadPwaInstall()
    expect(pwa.loadDismissed()).toBe(false)
    expect(() => pwa.dismiss()).not.toThrow()
  })
})

describe('installAvailable', () => {
  it('offers nothing until the browser says an install is possible', async () => {
    const pwa = await loadPwaInstall()
    expect(pwa.installAvailable()).toBe(false)
  })

  it('offers the install the browser reports', async () => {
    const pwa = await loadPwaInstall()
    fireInstallPrompt()
    expect(pwa.installAvailable()).toBe(true)
  })

  it('suppresses the browser mini-infobar', async () => {
    await loadPwaInstall()
    expect(fireInstallPrompt().defaultPrevented).toBe(true)
  })

  it('stays quiet on an offer this browser has waved away', async () => {
    localStorage.setItem(DISMISSED_KEY, '1')
    const pwa = await loadPwaInstall()
    fireInstallPrompt()
    expect(pwa.installAvailable()).toBe(false)
  })

  it('drops the offer an install through the browser menu settled', async () => {
    const pwa = await loadPwaInstall()
    fireInstallPrompt()
    globalThis.dispatchEvent(new Event('appinstalled'))
    expect(pwa.installAvailable()).toBe(false)
  })
})

describe('install', () => {
  it('prompts the browser and drops the offer an install accepted', async () => {
    const pwa = await loadPwaInstall()
    const event = fireInstallPrompt('accepted')
    await pwa.install()
    expect(event.prompt).toHaveBeenCalledOnce()
    expect(pwa.installAvailable()).toBe(false)
    expect(localStorage.getItem(DISMISSED_KEY)).toBeNull()
  })

  it('does nothing where the browser offered no install', async () => {
    const pwa = await loadPwaInstall()
    await expect(pwa.install()).resolves.toBeUndefined()
    expect(pwa.installAvailable()).toBe(false)
  })

  it('records nothing when the user declines the browser dialog', async () => {
    const pwa = await loadPwaInstall()
    fireInstallPrompt('dismissed')
    await pwa.install()
    expect(pwa.installAvailable()).toBe(false)
    expect(pwa.loadDismissed()).toBe(false)
    expect(localStorage.getItem(DISMISSED_KEY)).toBeNull()
  })

  it('offers again when the browser fires a second event', async () => {
    const pwa = await loadPwaInstall()
    fireInstallPrompt('dismissed')
    await pwa.install()
    fireInstallPrompt()
    expect(pwa.installAvailable()).toBe(true)
  })
})

describe('usePwaInstall', () => {
  it('shows and hides the banner as the offer arrives and is dismissed', async () => {
    const pwa = await loadPwaInstall()
    const seen = mount(pwa.usePwaInstall)
    expect(seen.current.available).toBe(false)

    act(() => void fireInstallPrompt())
    expect(seen.current.available).toBe(true)
    expect(seen.current.dismissed).toBe(false)

    act(() => seen.current.dismiss())
    expect(seen.current.available).toBe(false)
    expect(seen.current.dismissed).toBe(true)
  })

  it('follows a dismissal another tab made', async () => {
    const pwa = await loadPwaInstall()
    const seen = mount(pwa.usePwaInstall)
    act(() => void fireInstallPrompt())
    expect(seen.current.available).toBe(true)

    localStorage.setItem(DISMISSED_KEY, '1')
    act(() => {
      globalThis.dispatchEvent(
        new StorageEvent('storage', { key: DISMISSED_KEY, newValue: '1' }),
      )
    })
    expect(seen.current.available).toBe(false)
    expect(seen.current.dismissed).toBe(true)
  })

  it('ignores another tab writing an unrelated key', async () => {
    const pwa = await loadPwaInstall()
    const seen = mount(pwa.usePwaInstall)
    act(() => void fireInstallPrompt())

    act(() => {
      globalThis.dispatchEvent(
        new StorageEvent('storage', { key: 'trau.inbox.seen', newValue: '{}' }),
      )
    })
    expect(seen.current.available).toBe(true)
  })
})
