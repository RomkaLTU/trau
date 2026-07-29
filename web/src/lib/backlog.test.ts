import { describe, expect, it } from 'vitest'

import {
  backlogSections,
  hiddenStateGroups,
  nestBacklogRows,
  rowExpandable,
  rowProgress,
  sectionLabel,
  type BacklogEntry,
  type BacklogRowNode,
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

function epic(id: string, group: string, settled: number, total: number): BacklogEntry {
  return {
    ...entry(id, group),
    has_children: true,
    children_settled: settled,
    children_total: total,
  }
}

function child(id: string, group: string, parent: string): BacklogEntry {
  return { ...entry(id, group), parent }
}

function shape(nodes: BacklogRowNode[]): unknown[] {
  return nodes.map((node) => [node.entry.id, shape(node.children)])
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

describe('nestBacklogRows', () => {
  it('nests children under their parent row', () => {
    const rows = [
      epic('COD-1', 'backlog', 1, 3),
      child('COD-2', 'backlog', 'COD-1'),
      child('COD-3', 'backlog', 'COD-1'),
    ]
    expect(shape(nestBacklogRows(rows))).toEqual([
      [
        'COD-1',
        [
          ['COD-2', []],
          ['COD-3', []],
        ],
      ],
    ])
  })

  it('keeps a standalone issue and separate epics apart', () => {
    const rows = [
      epic('COD-1', 'backlog', 0, 2),
      child('COD-2', 'backlog', 'COD-1'),
      entry('COD-9', 'backlog'),
      epic('COD-4', 'backlog', 2, 2),
      child('COD-5', 'backlog', 'COD-4'),
    ]
    expect(shape(nestBacklogRows(rows))).toEqual([
      ['COD-1', [['COD-2', []]]],
      ['COD-9', []],
      ['COD-4', [['COD-5', []]]],
    ])
  })

  it('leaves a child flat when its parent row is absent from the section', () => {
    const rows = [child('COD-2', 'started', 'COD-1'), entry('COD-8', 'started')]
    expect(shape(nestBacklogRows(rows))).toEqual([
      ['COD-2', []],
      ['COD-8', []],
    ])
  })

  it('nests a child under a parent it is not adjacent to', () => {
    const rows = [
      epic('COD-1', 'backlog', 0, 1),
      entry('COD-9', 'backlog'),
      child('COD-2', 'backlog', 'COD-1'),
    ]
    expect(shape(nestBacklogRows(rows))).toEqual([
      ['COD-1', [['COD-2', []]]],
      ['COD-9', []],
    ])
  })

  it('nests a family to arbitrary depth, rendering each row once', () => {
    const rows = [
      epic('TMS-1018', 'started', 0, 1),
      { ...epic('TMS-1212', 'started', 0, 1), parent: 'TMS-1018' },
      { ...epic('TMS-1339', 'started', 1, 1), parent: 'TMS-1212' },
      child('TMS-1401', 'started', 'TMS-1339'),
    ]
    expect(shape(nestBacklogRows(rows))).toEqual([
      ['TMS-1018', [['TMS-1212', [['TMS-1339', [['TMS-1401', []]]]]]]],
    ])
  })

  it('returns an empty list for no rows', () => {
    expect(nestBacklogRows([])).toEqual([])
  })
})

describe('rowExpandable', () => {
  it('opens any row that has nested rows, whatever its stored flag says', () => {
    const [root] = nestBacklogRows([
      epic('TMS-1018', 'started', 0, 1),
      child('TMS-1212', 'started', 'TMS-1018'),
      child('TMS-1340', 'started', 'TMS-1212'),
    ])
    expect(rowExpandable(root)).toBe(true)
    expect(rowExpandable(root.children[0])).toBe(true)
    expect(rowExpandable(root.children[0].children[0])).toBe(false)
  })
})

describe('rowProgress', () => {
  it('reports the counts the store sent for a parent with children', () => {
    expect(rowProgress(epic('COD-1', 'backlog', 2, 5))).toEqual({ settled: 2, total: 5 })
  })

  it('reports nothing for a parent with no children left to count', () => {
    expect(rowProgress(epic('COD-1', 'backlog', 0, 0))).toBeNull()
    expect(rowProgress(entry('COD-2', 'backlog'))).toBeNull()
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
