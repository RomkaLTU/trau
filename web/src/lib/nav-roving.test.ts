import { describe, expect, it } from 'vitest'

import { NAV_GROUPS } from '@/components/trau/nav-items'

import { activeNavIndex, navOrder, rovingIndex } from './nav-roving'

const items = navOrder()

describe('navOrder', () => {
  it('keeps every group’s items in view order and nothing else', () => {
    expect(items.map((item) => item.label)).toEqual(
      NAV_GROUPS.flatMap((group) => group.items).map((item) => item.label),
    )
    expect(items).toHaveLength(14)
  })

  it('holds the gated items too, so they stay reachable by arrow', () => {
    expect(items.filter((item) => item.requiresProject).length).toBeGreaterThan(
      0,
    )
    expect(items.map((item) => item.label)).toContain('Terminal')
  })

  it('carries no group header into the order', () => {
    const headers = NAV_GROUPS.map((group) => group.label)
    expect(items.some((item) => headers.includes(item.label))).toBe(false)
  })
})

describe('rovingIndex', () => {
  it('steps down and up one item at a time', () => {
    expect(rovingIndex('ArrowDown', 0, 14)).toBe(1)
    expect(rovingIndex('ArrowUp', 5, 14)).toBe(4)
  })

  it('clamps at both ends instead of wrapping', () => {
    expect(rovingIndex('ArrowDown', 13, 14)).toBe(13)
    expect(rovingIndex('ArrowUp', 0, 14)).toBe(0)
  })

  it('jumps to the first and last item', () => {
    expect(rovingIndex('Home', 7, 14)).toBe(0)
    expect(rovingIndex('End', 7, 14)).toBe(13)
  })

  it('pulls an out-of-range position back into the list first', () => {
    expect(rovingIndex('ArrowDown', -3, 14)).toBe(1)
    expect(rovingIndex('ArrowUp', 99, 14)).toBe(12)
  })

  it('leaves keys it does not own alone', () => {
    expect(rovingIndex('Enter', 3, 14)).toBeNull()
    expect(rovingIndex(' ', 3, 14)).toBeNull()
    expect(rovingIndex('Tab', 3, 14)).toBeNull()
    expect(rovingIndex('ArrowRight', 3, 14)).toBeNull()
  })

  it('has nowhere to go in an empty nav', () => {
    expect(rovingIndex('ArrowDown', 0, 0)).toBeNull()
    expect(rovingIndex('Home', 0, 0)).toBeNull()
  })
})

describe('activeNavIndex', () => {
  it('lands on the item the route is on', () => {
    expect(items[activeNavIndex('/loop', items)].label).toBe('Loop')
    expect(items[activeNavIndex('/settings', items)].label).toBe('Settings')
  })

  it('keeps a nested route on its section', () => {
    expect(items[activeNavIndex('/runs/loop/COD-1548', items)].label).toBe(
      'Runs',
    )
  })

  it('holds Overview to an exact match', () => {
    expect(items[activeNavIndex('/', items)].label).toBe('Overview')
    expect(items[activeNavIndex('/backlog', items)].label).toBe('Backlog')
  })

  it('falls back to the first item on a page the sidebar omits', () => {
    expect(activeNavIndex('/app-urls', items)).toBe(0)
  })
})
