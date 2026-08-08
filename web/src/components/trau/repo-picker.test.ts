// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'

import { RepoPicker } from './repo-picker'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {}
}
if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

let root: Root | undefined

function render() {
  const chosen: string[] = []
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => {
    mounted.render(
      createElement(RepoPicker, {
        repos: ['claude', 'codex', 'cursor'],
        value: 'codex',
        label: 'provider',
        onChange: (next: string) => chosen.push(next),
      }),
    )
  })
  return chosen
}

function trigger(): HTMLButtonElement {
  return document.body.querySelector('button') as HTMLButtonElement
}

function open() {
  act(() => {
    trigger().focus()
    trigger().click()
  })
}

function options(): HTMLElement[] {
  return [...document.body.querySelectorAll<HTMLElement>('[cmdk-item=""]')]
}

function input(): HTMLInputElement {
  return document.body.querySelector('[cmdk-input=""]') as HTMLInputElement
}

function key(name: string) {
  act(() => {
    input().dispatchEvent(
      new KeyboardEvent('keydown', { key: name, bubbles: true }),
    )
  })
}

function type(query: string) {
  const setValue = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )?.set
  act(() => {
    setValue?.call(input(), query)
    input().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function highlighted(): string | null {
  const selected = options().filter(
    (el) => el.getAttribute('aria-selected') === 'true',
  )
  expect(selected).toHaveLength(1)
  return selected[0].getAttribute('data-value')
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

describe('RepoPicker', () => {
  it('marks the trigger as a collapsed popup until it opens', () => {
    render()

    expect(trigger().getAttribute('aria-expanded')).toBe('false')
    open()
    expect(trigger().getAttribute('aria-expanded')).toBe('true')
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull()
  })

  it('moves a highlight with the arrows and selects it with Enter', () => {
    const chosen = render()
    open()

    expect(highlighted()).toBe('claude')
    key('ArrowDown')
    expect(highlighted()).toBe('codex')
    key('ArrowDown')
    expect(highlighted()).toBe('cursor')
    key('Enter')

    expect(chosen).toEqual(['cursor'])
    expect(options()).toHaveLength(0)
  })

  it('filters the options as the search is typed', () => {
    render()
    open()

    type('cur')
    expect(options().map((el) => el.getAttribute('data-value'))).toEqual([
      'cursor',
    ])
  })

  it('closes on Escape and hands focus back to the trigger', async () => {
    render()
    open()

    await act(async () => {
      document.body
        .querySelector('[data-slot="popover-content"]')
        ?.dispatchEvent(
          new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }),
        )
    })

    // Radix hands focus back on a deferred tick, so flush one before looking.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })

    expect(options()).toHaveLength(0)
    expect(document.activeElement).toBe(trigger())
  })
})
