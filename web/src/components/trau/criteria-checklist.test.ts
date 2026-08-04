// @vitest-environment happy-dom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { CriteriaChecklist } from '@/components/trau/criteria-checklist'
import type { CriterionStatus, VerdictCriterion } from '@/lib/rundetail'

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

function rows(criteria: VerdictCriterion[]): HTMLLIElement[] {
  act(() => root.render(createElement(CriteriaChecklist, { criteria })))
  return Array.from(container.querySelectorAll('li'))
}

it('marks each criterion with its own grade', () => {
  const items = rows([
    { text: 'the export button disables while saving', status: 'satisfied' },
    { text: 'the list paginates at 50', status: 'violated', note: 'off by one' },
    {
      text: 'the toast reads in local time',
      status: 'unverified',
      note: 'browser undriven',
    },
  ])
  expect(items).toHaveLength(3)
  expect(items.map((li) => li.firstElementChild?.textContent)).toEqual([
    '✓',
    '✗',
    '?',
  ])
  expect(items[2].textContent).toContain('the toast reads in local time')
  expect(items[2].textContent).toContain('browser undriven')
  const tones = items.map((li) => li.firstElementChild!.className)
  expect(tones[0]).toContain('text-done')
  expect(tones[2]).toContain('text-warn')
})

it('reads an ungated status as unverified', () => {
  const items = rows([
    { text: 'derived from the diff', status: 'n/a' as CriterionStatus },
  ])
  expect(items[0].firstElementChild?.textContent).toBe('?')
  expect(items[0].firstElementChild!.className).toContain('text-warn')
  expect(items[0].textContent).toContain('unverified: ')
})
