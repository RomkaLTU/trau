// @vitest-environment happy-dom
import { act, createElement, useState, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, expect, it } from 'vitest'

import { useRovingGroup } from './use-roving-group'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true

const OPTIONS = ['one', 'two', 'three']

let root: Root | undefined

function Group({ withField }: { withField?: boolean }) {
  const [picked, setPicked] = useState('two')
  const roving = useRovingGroup({
    keys: OPTIONS,
    selected: picked,
    onSelect: setPicked,
  })
  const children: ReactNode[] = OPTIONS.map((option) =>
    createElement(
      'button',
      {
        key: option,
        type: 'button',
        role: 'radio',
        'data-key': option,
        'aria-checked': option === picked,
        ...roving.itemProps(option),
        onClick: () => setPicked(option),
      },
      option,
    ),
  )
  if (withField) {
    children.push(createElement('input', { key: 'field', 'aria-label': 'name' }))
  }
  return createElement(
    'div',
    { role: 'radiogroup', 'aria-label': 'Group', ...roving.groupProps },
    children,
  )
}

function render(props: { withField?: boolean } = {}) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  act(() => mounted.render(createElement(Group, props)))
}

function items(): HTMLElement[] {
  return [...document.body.querySelectorAll<HTMLElement>('[role="radio"]')]
}

function checked(): string {
  return items()
    .map((el) => (el.getAttribute('aria-checked') === 'true' ? 'X' : '.'))
    .join('')
}

function stops(): number[] {
  return items().map((el) => el.tabIndex)
}

function focused(): number {
  return items().indexOf(document.activeElement as HTMLElement)
}

function press(key: string, over: KeyboardEventInit = {}) {
  act(() => {
    document.activeElement?.dispatchEvent(
      new KeyboardEvent('keydown', { key, bubbles: true, ...over }),
    )
  })
}

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

it('gives the group one Tab stop, on the selected item', () => {
  render()

  expect(stops()).toEqual([-1, 0, -1])
  expect(checked()).toBe('.X.')
})

it('moves and selects on either forward arrow', () => {
  render()
  items()[1].focus()

  press('ArrowRight')
  expect(focused()).toBe(2)
  expect(checked()).toBe('..X')
  expect(stops()).toEqual([-1, -1, 0])

  press('ArrowDown')
  expect(focused()).toBe(0)
  expect(checked()).toBe('X..')
})

it('moves and selects on either back arrow, wrapping at the start', () => {
  render()
  items()[1].focus()

  press('ArrowLeft')
  expect(focused()).toBe(0)
  expect(checked()).toBe('X..')

  press('ArrowUp')
  expect(focused()).toBe(2)
  expect(checked()).toBe('..X')
})

it('jumps to the ends on Home and End', () => {
  render()
  items()[1].focus()

  press('End')
  expect(focused()).toBe(2)
  expect(checked()).toBe('..X')

  press('Home')
  expect(focused()).toBe(0)
  expect(checked()).toBe('X..')
})

it('leaves a modified arrow alone', () => {
  render()
  items()[1].focus()

  press('ArrowRight', { shiftKey: true })
  press('ArrowRight', { metaKey: true })
  press('ArrowRight', { ctrlKey: true })
  press('ArrowRight', { altKey: true })

  expect(focused()).toBe(1)
  expect(checked()).toBe('.X.')
})

it('leaves the arrows to a field inside the group', () => {
  render({ withField: true })
  const field = document.body.querySelector<HTMLInputElement>('input')
  field?.focus()

  press('ArrowRight')

  expect(document.activeElement).toBe(field)
  expect(checked()).toBe('.X.')
})

it('parks the Tab stop on the first item while nothing is selected', () => {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const mounted = createRoot(container)
  root = mounted
  function Unselected() {
    const roving = useRovingGroup({
      keys: OPTIONS,
      selected: null,
      onSelect: () => {},
    })
    return createElement(
      'div',
      { role: 'radiogroup' },
      OPTIONS.map((option) =>
        createElement(
          'button',
          {
            key: option,
            type: 'button',
            role: 'radio',
            'aria-checked': false,
            ...roving.itemProps(option),
          },
          option,
        ),
      ),
    )
  }
  act(() => mounted.render(createElement(Unselected)))

  expect(stops()).toEqual([0, -1, -1])
})
