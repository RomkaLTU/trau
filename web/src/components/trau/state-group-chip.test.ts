// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { StateGroupChip } from '@/components/trau/state-group-chip'
import { STATE_GROUPS } from '@/lib/backlog'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
})

function chip(group: string): HTMLElement {
  act(() => root.render(createElement(StateGroupChip, { group })))
  return container.firstElementChild as HTMLElement
}

function chipClass(group: string): string {
  return chip(group).className
}

it('renders the raw group string', () => {
  for (const group of STATE_GROUPS) {
    expect(chip(group).textContent).toBe(group)
  }
  expect(chip('mystery').textContent).toBe('mystery')
})

it('colors done violet, clear of the ready and queued chips', () => {
  const classes = chipClass('done')
  expect(classes).toContain('text-violet-600')
  expect(classes).toContain('dark:text-violet-400')
  expect(classes).not.toContain('emerald')
  expect(classes).not.toContain('sky')
})

it('keeps the colored groups apart and the two neutral ones legible', () => {
  const colored = ['started', 'done', 'canceled'].map(chipClass)
  expect(new Set(colored).size).toBe(colored.length)
  expect(chipClass('backlog')).not.toBe(chipClass('unstarted'))
})

it('falls back to the unstarted treatment for an unmapped group', () => {
  expect(chipClass('mystery')).toBe(chipClass('unknown'))
  expect(chipClass('unknown')).toBe(chipClass('unstarted'))
})
