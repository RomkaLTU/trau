import { describe, expect, it } from 'vitest'

import { groupBacklog, type BacklogEntry } from './backlog'

function entry(over: Partial<BacklogEntry>): BacklogEntry {
  return {
    id: 'COD-1',
    title: 'x',
    status: 'Todo',
    group: 'unstarted',
    labels: [],
    has_children: false,
    ready: false,
    ...over,
  }
}

describe('groupBacklog', () => {
  it('orders groups by board order and drops empty ones', () => {
    const groups = groupBacklog([
      entry({ id: 'COD-1', group: 'done' }),
      entry({ id: 'COD-2', group: 'started' }),
      entry({ id: 'COD-3', group: 'unstarted' }),
    ])
    expect(groups.map((g) => g.key)).toEqual(['started', 'unstarted', 'done'])
    expect(groups.map((g) => g.label)).toEqual(['In progress', 'Todo', 'Done'])
  })

  it('keeps epics first, then most recent issue number, within a group', () => {
    const groups = groupBacklog([
      entry({ id: 'COD-10', group: 'unstarted' }),
      entry({ id: 'COD-20', group: 'unstarted', has_children: true }),
      entry({ id: 'COD-30', group: 'unstarted' }),
    ])
    expect(groups).toHaveLength(1)
    expect(groups[0].items.map((i) => i.id)).toEqual([
      'COD-20',
      'COD-30',
      'COD-10',
    ])
  })

  it('buckets an unrecognized status group under Other', () => {
    const groups = groupBacklog([entry({ id: 'COD-9', group: 'weird' })])
    expect(groups.map((g) => g.key)).toEqual(['unknown'])
    expect(groups[0].label).toBe('Other')
  })
})
