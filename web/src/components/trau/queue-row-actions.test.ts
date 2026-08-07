// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import { QueueRowAction } from '@/components/trau/queue-row-actions'
import type { QueueItem } from '@/lib/queue'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

function item(over: Partial<QueueItem> = {}): QueueItem {
  return { position: 1, kind: 'ticket', id: 'COD-1543', status: 'pending', ...over }
}

let root: Root | undefined

function renderRow(over: Partial<QueueItem>, stopping = false) {
  const stops: number[] = []
  const removed: string[] = []
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => {
    mounted.render(
      createElement(QueueRowAction, {
        item: item(over),
        busy: false,
        stopping,
        onRemove: (id: string) => removed.push(id),
        onStop: () => stops.push(1),
      }),
    )
  })
  return { stops, removed }
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

function byLabel(label: string): HTMLElement | null {
  return document.body.querySelector<HTMLElement>(`[aria-label="${label}"]`)
}

function stopButton(): HTMLElement {
  const found = byLabel('Stop the run (progress is saved — resumable with Start)')
  if (!found) throw new Error('no Stop control rendered')
  return found
}

function dialogText(): string {
  return document.body.querySelector('[role="alertdialog"]')?.textContent ?? ''
}

function confirmButton(label: string): HTMLElement {
  const found = [...document.body.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label,
  )
  if (!found) throw new Error(`no button labelled ${label}`)
  return found
}

describe('a running row', () => {
  it('offers Stop and never the destructive X', () => {
    renderRow({ status: 'running' })

    expect(stopButton()).toBeTruthy()
    expect(byLabel('Remove COD-1543 from queue')).toBeNull()
  })

  it('opens a confirm that names the ticket, its saved progress and the two-step removal', () => {
    renderRow({ status: 'running' })

    act(() => {
      stopButton().click()
    })

    const text = dialogText()
    expect(text).toContain('Stop COD-1543?')
    expect(text).toContain('saved at the last checkpoint')
    expect(text).toContain('COD-1543 stays resumable')
    expect(text).toContain('stop it first, then remove the parked row')
    expect(text).not.toContain('Pause')
  })

  it('stops the run when the confirm is taken', () => {
    const { stops } = renderRow({ status: 'running' })

    act(() => {
      stopButton().click()
    })
    act(() => {
      confirmButton('Stop').click()
    })

    expect(stops).toHaveLength(1)
  })

  it('reads as stopping and opens nothing while the stop is in flight', () => {
    renderRow({ status: 'running' }, true)

    const control = byLabel('Stopping COD-1543…')
    expect(control).toBeTruthy()
    expect((control as HTMLButtonElement).disabled).toBe(true)

    act(() => {
      control?.click()
    })
    expect(document.body.querySelector('[role="alertdialog"]')).toBeNull()
  })
})

describe('every other row', () => {
  it('keeps the X and never offers Stop', () => {
    for (const status of ['pending', 'paused', 'failed', 'done']) {
      renderRow({ status })

      expect(byLabel('Remove COD-1543 from queue')).toBeTruthy()
      expect(
        byLabel('Stop the run (progress is saved — resumable with Start)'),
      ).toBeNull()

      act(() => root?.unmount())
      root = undefined
      document.body.innerHTML = ''
    }
  })

  it('asks for the removal the row was pressed on', () => {
    const { removed } = renderRow({ status: 'paused' })

    act(() => {
      byLabel('Remove COD-1543 from queue')?.click()
    })

    expect(removed).toEqual(['COD-1543'])
  })
})
