import { describe, expect, it } from 'vitest'

import {
  backlogSections,
  hiddenStateGroups,
  sectionLabel,
  type BacklogEntry,
} from './backlog'

function entry(id: string, group: string): BacklogEntry {
  return {
    id,
    title: id,
    status: group,
    group,
    labels: [],
    source: 'linear',
    has_children: false,
    ready: false,
  }
}

describe('sectionLabel', () => {
  it('maps status groups to their board headers', () => {
    expect(sectionLabel('started')).toBe('In Progress')
    expect(sectionLabel('unstarted')).toBe('Todo')
    expect(sectionLabel('unknown')).toBe('Other')
  })

  it('falls back to the raw group for an unmapped value', () => {
    expect(sectionLabel('mystery')).toBe('mystery')
  })
})

describe('backlogSections', () => {
  it('splits contiguous groups into sections in row order', () => {
    const items = [
      entry('A-1', 'started'),
      entry('A-2', 'started'),
      entry('A-3', 'unstarted'),
      entry('A-4', 'backlog'),
    ]
    const counts = { started: 12, unstarted: 5, backlog: 40, done: 300 }

    expect(
      backlogSections(items, counts).map((s) => [s.group, s.label, s.count, s.items.length]),
    ).toEqual([
      ['started', 'In Progress', 12, 2],
      ['unstarted', 'Todo', 5, 1],
      ['backlog', 'Backlog', 40, 1],
    ])
  })

  it('takes the header count from counts, not the on-page row count', () => {
    const [section] = backlogSections([entry('A-1', 'unstarted')], { unstarted: 12 })
    expect(section.count).toBe(12)
    expect(section.items).toHaveLength(1)
  })

  it('falls back to zero when counts omits the group', () => {
    const [section] = backlogSections([entry('X-1', 'unknown')], {})
    expect(section).toMatchObject({ group: 'unknown', label: 'Other', count: 0 })
  })

  it('returns nothing for an empty page', () => {
    expect(backlogSections([], { done: 3 })).toEqual([])
  })

  it('marks no section as a continuation on the first page', () => {
    const items = [entry('D-1', 'done'), entry('D-2', 'done')]
    const [section] = backlogSections(items, { done: 232 }, ['done'], 0)
    expect(section.continuation).toBe(false)
  })

  it('flags the leading section as a continuation when its group spans a page boundary', () => {
    const items = [entry('D-51', 'done'), entry('D-52', 'done')]
    const [section] = backlogSections(items, { done: 232 }, ['done'], 50)
    expect(section).toMatchObject({ group: 'done', continuation: true })
  })

  it('keeps the header when a page starts exactly at a group boundary', () => {
    const items = [entry('D-1', 'done')]
    const [section] = backlogSections(items, { unstarted: 50, done: 232 }, ['unstarted', 'done'], 50)
    expect(section.continuation).toBe(false)
  })

  it('flags a continuation across preceding groups on a later page', () => {
    const items = [entry('D-6', 'done'), entry('D-7', 'done')]
    const [section] = backlogSections(items, { unstarted: 45, done: 232 }, ['unstarted', 'done'], 50)
    expect(section.continuation).toBe(true)
  })
})

describe('hiddenStateGroups', () => {
  const counts = { started: 12, unstarted: 5, done: 300, canceled: 14 }

  it('reports done and canceled when the planned default hides them', () => {
    expect(
      hiddenStateGroups(counts, ['started', 'unstarted', 'backlog', 'unknown']),
    ).toEqual([
      { group: 'done', count: 300 },
      { group: 'canceled', count: 14 },
    ])
  })

  it('drops a group the selection already shows', () => {
    expect(hiddenStateGroups(counts, ['done'])).toEqual([{ group: 'canceled', count: 14 }])
  })

  it('drops a group with no matches', () => {
    expect(hiddenStateGroups({ done: 0, canceled: 3 }, ['started'])).toEqual([
      { group: 'canceled', count: 3 },
    ])
  })

  it('reports nothing when both terminal groups are shown', () => {
    expect(hiddenStateGroups(counts, ['done', 'canceled'])).toEqual([])
  })
})
