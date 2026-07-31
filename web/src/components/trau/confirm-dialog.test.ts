// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { ConfirmDialog } from '@/components/trau/confirm-dialog'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

const members = Array.from({ length: 44 }, (_, i) => `acme/repo-${i}`)

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() =>
    root.render(
      createElement(ConfirmDialog, {
        open: true,
        windowTitle: 'remove project',
        title: 'Remove acme from the hub?',
        description: createElement(
          'span',
          { className: 'flex flex-col' },
          members.map((name) => createElement('span', { key: name }, name)),
        ),
        confirmLabel: 'Remove all',
        onConfirm: () => {},
      }),
    ),
  )
})

afterEach(() => {
  act(() => root.unmount())
  document.body.innerHTML = ''
})

function scrollBody(): HTMLElement {
  const found = document.body.querySelectorAll<HTMLElement>(
    '[data-slot="confirm-dialog-body"]',
  )
  expect(found).toHaveLength(1)
  return found[0]
}

function slot(name: string): HTMLElement {
  const found = document.body.querySelector<HTMLElement>(
    `[data-slot="${name}"]`,
  )
  if (!found) throw new Error(`no ${name} rendered`)
  return found
}

function button(label: string): HTMLElement {
  const found = [...document.body.querySelectorAll('button')].find(
    (b) => b.textContent?.trim() === label,
  )
  if (!found) throw new Error(`no button labelled ${label}`)
  return found
}

it('scrolls the description inside one region', () => {
  expect(scrollBody().className).toContain('overflow-y-auto')
  expect(scrollBody().textContent).toContain('acme/repo-43')
})

it('leaves the question and the buttons out of the scroll region', () => {
  const scroller = scrollBody()
  expect(scroller.contains(slot('alert-dialog-title'))).toBe(false)
  expect(scroller.contains(button('Remove all'))).toBe(false)
  expect(scroller.contains(button('Cancel'))).toBe(false)
})

it('caps the dialog height', () => {
  expect(slot('alert-dialog-content').className).toContain('max-h-[')
})
