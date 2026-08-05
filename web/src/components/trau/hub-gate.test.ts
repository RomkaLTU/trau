// @vitest-environment happy-dom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi, type Mock } from 'vitest'

import { HubGate } from '@/components/trau/hub-gate'
import { resetHubWatch } from '@/lib/connectivity'
import { killHub, stubHub } from '@/lib/connectivity-fixtures'

let container: HTMLDivElement
let root: Root
let client: QueryClient
let onRecover: Mock<() => void>

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

function mount() {
  act(() =>
    root.render(
      createElement(
        QueryClientProvider,
        { client },
        createElement(HubGate, { onRecover }),
      ),
    ),
  )
}

function retryButton(): HTMLButtonElement | undefined {
  return [...container.querySelectorAll('button')].find((el) =>
    el.textContent?.includes('Retry now'),
  )
}

function copyButton(): HTMLButtonElement | null {
  return container.querySelector(
    'button[aria-label="Copy the hub start command"]',
  )
}

function setClipboard(clipboard: { writeText: Mock } | undefined) {
  Object.defineProperty(navigator, 'clipboard', {
    value: clipboard,
    configurable: true,
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  client = new QueryClient()
  onRecover = vi.fn()
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  resetHubWatch()
  setClipboard(undefined)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it('stays out of the way while the hub answers', async () => {
  stubHub(() => true)
  mount()
  await act(() => vi.advanceTimersByTimeAsync(0))
  expect(container.textContent).toBe('')
})

it('names the hub, the command that starts it, and the retry countdown', () => {
  stubHub(() => false)
  mount()
  act(killHub)

  const text = container.textContent ?? ''
  expect(text).toContain('Hub unreachable')
  expect(text).toContain('trau hub start')
  expect(text).not.toContain('trau serve')
  expect(text).toMatch(/retrying in \ds/)
  expect(container.querySelector('code')?.textContent).toBe('trau hub start')
  expect(copyButton()).not.toBeNull()
  expect(container.querySelector('[role="alert"]')).not.toBeNull()
})

it('copies the start command and says so', async () => {
  const writeText = vi.fn(async () => {})
  setClipboard({ writeText })
  stubHub(() => false)
  mount()
  act(killHub)

  await act(async () => {
    copyButton()?.click()
    await vi.advanceTimersByTimeAsync(0)
  })

  expect(writeText).toHaveBeenCalledWith('trau hub start')
  expect(container.querySelector('[role="status"]')?.textContent).toBe('copied')
})

it('says so when the copy fails', async () => {
  setClipboard(undefined)
  document.execCommand = vi.fn(() => false)
  stubHub(() => false)
  mount()
  act(killHub)

  await act(async () => {
    copyButton()?.click()
    await vi.advanceTimersByTimeAsync(0)
  })

  expect(container.querySelector('[role="status"]')?.textContent).toBe(
    'copy failed',
  )
})

it('takes the whole viewport, above every dialog and dock, and stays clickable under one', () => {
  stubHub(() => false)
  mount()
  act(killHub)

  const screen = container.querySelector('[role="alert"]') as HTMLElement
  expect(screen.className).toContain('fixed inset-0')
  expect(screen.className).toContain('z-[120]')
  expect(screen.className).toContain('pointer-events-auto')
})

it('clears itself, refetches and resets the route matches once the hub is back', async () => {
  let listening = false
  stubHub(() => listening)
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  mount()
  act(killHub)
  expect(container.textContent).toContain('Hub unreachable')

  listening = true
  await act(() => vi.advanceTimersByTimeAsync(1000))
  expect(container.textContent).toBe('')
  expect(invalidate).toHaveBeenCalled()
  expect(onRecover).toHaveBeenCalled()
})

it('probes on demand from the retry button', async () => {
  let listening = false
  stubHub(() => listening)
  mount()
  act(killHub)

  listening = true
  await act(async () => {
    retryButton()?.click()
    await vi.advanceTimersByTimeAsync(0)
  })
  expect(container.textContent).toBe('')
})

it('keeps its own clicks from reaching the dismissable layers underneath', () => {
  stubHub(() => false)
  mount()
  act(killHub)

  const onDocument = vi.fn()
  document.addEventListener('pointerdown', onDocument)
  retryButton()?.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
  document.removeEventListener('pointerdown', onDocument)

  expect(onDocument).not.toHaveBeenCalled()
})
