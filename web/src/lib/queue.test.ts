import { describe, expect, it } from 'vitest'

import { queueCounts, type QueueItem } from './queue'

function item(over: Partial<QueueItem>): QueueItem {
  return {
    position: 1,
    kind: 'ticket',
    id: 'COD-1',
    status: 'pending',
    ...over,
  }
}

describe('queueCounts', () => {
  it('counts an empty queue as all zeros', () => {
    expect(queueCounts([])).toEqual({ total: 0, tickets: 0, epics: 0 })
  })

  it('splits the total between tickets and epics', () => {
    const counts = queueCounts([
      item({ id: 'COD-1', kind: 'ticket' }),
      item({ id: 'COD-2', kind: 'epic' }),
      item({ id: 'COD-3', kind: 'ticket' }),
    ])
    expect(counts).toEqual({ total: 3, tickets: 2, epics: 1 })
  })
})
