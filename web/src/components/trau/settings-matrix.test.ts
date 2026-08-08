// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it } from 'vitest'

import type { ConfigKey } from '@/lib/config'
import { PhaseMatrix } from './settings-matrix'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

const KEYS: ConfigKey[] = [
  'CLAUDE_BUILD_MODEL',
  'CODEX_BUILD_MODEL',
  'KIMI_BUILD_MODEL',
].map((key) => ({ key, value: 'x', layer: 'default', editable: true }))

let root: Root | undefined

function render() {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() =>
    mounted.render(
      createElement(PhaseMatrix, {
        keys: KEYS,
        repo: null,
        layers: ['default'],
        hubRestart: false,
        editingKey: null,
        onEdit: () => {},
        onCancel: () => {},
        onSaved: () => {},
      }),
    ),
  )
}

function tabs(): HTMLElement[] {
  return [...document.body.querySelectorAll<HTMLElement>('[role="tab"]')]
}

function selected(): string {
  return tabs()
    .map((el) => (el.getAttribute('aria-selected') === 'true' ? 'X' : '.'))
    .join('')
}

function press(key: string) {
  act(() => {
    document.activeElement?.dispatchEvent(
      new KeyboardEvent('keydown', { key, bubbles: true }),
    )
  })
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

it('gives the provider tabs one Tab stop and walks them with the arrows', () => {
  render()

  expect(tabs().map((el) => el.tabIndex)).toEqual([0, -1, -1])
  expect(selected()).toBe('X..')

  tabs()[0].focus()
  press('ArrowRight')
  expect(selected()).toBe('.X.')
  expect(tabs()[1]).toBe(document.activeElement)

  press('End')
  expect(selected()).toBe('..X')

  press('ArrowRight')
  expect(selected()).toBe('X..')
})
