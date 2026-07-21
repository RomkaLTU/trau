import { describe, expect, it } from 'vitest'

import {
  addTickets,
  pickerList,
  planAddSelected,
  toggleSelected,
  type AddTicketItem,
} from './add-ticket'
import type { QueueItem, QueueResponse } from './queue'
import type { SearchResult } from './search'

function result(over: Partial<SearchResult>): SearchResult {
  return {
    id: 'COD-1',
    title: 'a ticket',
    status: 'Todo',
    group: 'unstarted',
    source: 'linear',
    labels: [],
    has_children: false,
    ...over,
  }
}

function item(over: Partial<QueueItem>): QueueItem {
  return {
    position: 1,
    kind: 'ticket',
    id: 'COD-1',
    status: 'pending',
    ...over,
  }
}

function queueResponse(over: Partial<QueueResponse> = {}): QueueResponse {
  return { repo: 'trau', draining: false, shutting_down: false, items: [], ...over }
}

describe('pickerList', () => {
  it('offers every unsettled result when the queue is empty', () => {
    const a = result({ id: 'COD-1' })
    const b = result({ id: 'COD-2' })
    expect(pickerList([a, b], [])).toEqual({ rows: [a, b], empty: null })
  })

  it('drops results whose status group has settled', () => {
    const open = result({ id: 'COD-1', group: 'started' })
    const list = pickerList(
      [
        open,
        result({ id: 'COD-2', group: 'done' }),
        result({ id: 'COD-3', group: 'canceled' }),
      ],
      [],
    )
    expect(list).toEqual({ rows: [open], empty: null })
  })

  it('drops ids already in the queue', () => {
    const fresh = result({ id: 'COD-2' })
    const list = pickerList([result({ id: 'COD-1' }), fresh], [item({ id: 'COD-1' })])
    expect(list).toEqual({ rows: [fresh], empty: null })
  })

  it('drops a ticket covered by a queued epic sub-issue', () => {
    const list = pickerList(
      [result({ id: 'COD-3' })],
      [
        item({
          id: 'COD-100',
          kind: 'epic',
          sub_issues: [{ id: 'COD-3', title: 'x', state: 'todo' }],
        }),
      ],
    )
    expect(list).toEqual({ rows: [], empty: 'all-queued' })
  })

  it('reports no-match for an empty result set', () => {
    expect(pickerList([], [])).toEqual({ rows: [], empty: 'no-match' })
  })

  it('reports no-match — not all-queued — when every match had settled', () => {
    const list = pickerList([result({ id: 'COD-1', group: 'done' })], [])
    expect(list).toEqual({ rows: [], empty: 'no-match' })
  })

  it('reports all-queued when every unsettled match is already queued', () => {
    const list = pickerList(
      [result({ id: 'COD-1' }), result({ id: 'COD-2', group: 'done' })],
      [item({ id: 'COD-1' })],
    )
    expect(list).toEqual({ rows: [], empty: 'all-queued' })
  })
})

describe('toggleSelected', () => {
  it('checks a row that was not selected', () => {
    const row = result({ id: 'COD-1' })
    expect(toggleSelected([], row)).toEqual([row])
  })

  it('unchecks a row that was selected', () => {
    const row = result({ id: 'COD-1' })
    expect(toggleSelected([row], row)).toEqual([])
  })

  it('keeps the other picks and appends in check order', () => {
    const a = result({ id: 'COD-1' })
    const b = result({ id: 'COD-2' })
    expect(toggleSelected([a], b)).toEqual([a, b])
  })

  it('unchecks by id, leaving the rest in order', () => {
    const a = result({ id: 'COD-1' })
    const b = result({ id: 'COD-2' })
    const c = result({ id: 'COD-3' })
    expect(toggleSelected([a, b, c], b)).toEqual([a, c])
  })

  it('holds a pick the current results no longer list', () => {
    const stale = result({ id: 'COD-9' })
    const fresh = result({ id: 'COD-2' })
    expect(toggleSelected([stale], fresh)).toEqual([stale, fresh])
  })
})

describe('planAddSelected', () => {
  it('is empty for no selection', () => {
    expect(planAddSelected([])).toEqual([])
  })

  it('enqueues a childless result as a ticket and an epic as an epic', () => {
    expect(
      planAddSelected([
        result({ id: 'COD-1' }),
        result({ id: 'COD-100', has_children: true }),
      ]),
    ).toEqual([
      { id: 'COD-1', kind: 'ticket' },
      { id: 'COD-100', kind: 'epic' },
    ])
  })

  it('keeps the order the rows were checked in', () => {
    expect(
      planAddSelected([result({ id: 'COD-3' }), result({ id: 'COD-1' })]),
    ).toEqual([
      { id: 'COD-3', kind: 'ticket' },
      { id: 'COD-1', kind: 'ticket' },
    ])
  })
})

describe('addTickets', () => {
  const plan: AddTicketItem[] = [
    { id: 'COD-1', kind: 'ticket' },
    { id: 'COD-2', kind: 'epic' },
  ]

  it('enqueues every item in order', async () => {
    const seen: string[] = []
    await addTickets(
      plan,
      async (it) => {
        seen.push(it.id)
        return queueResponse()
      },
      () => {},
    )
    expect(seen).toEqual(['COD-1', 'COD-2'])
  })

  it('publishes each queue as it lands', async () => {
    const published: number[] = []
    await addTickets(
      plan,
      async (it) =>
        queueResponse({ items: [item({ id: it.id })] }),
      (res) => published.push(res.items.length),
    )
    expect(published).toEqual([1, 1])
  })

  it('enqueues the rest after one id fails, and reports the failure', async () => {
    const seen: string[] = []
    const published: string[] = []
    const run = addTickets(
      plan,
      async (it) => {
        seen.push(it.id)
        if (it.id === 'COD-1') throw new Error('already queued')
        return queueResponse({ items: [item({ id: it.id })] })
      },
      (res) => published.push(res.items[0].id),
    )
    await expect(run).rejects.toThrow('COD-1: already queued')
    expect(seen).toEqual(['COD-1', 'COD-2'])
    expect(published).toEqual(['COD-2'])
  })

  it('joins every failure into one report', async () => {
    const run = addTickets(
      plan,
      async (it) => {
        throw new Error(`${it.id} is gone`)
      },
      () => {},
    )
    await expect(run).rejects.toThrow('COD-1: COD-1 is gone\nCOD-2: COD-2 is gone')
  })

  it('reports a non-Error rejection as text', async () => {
    const run = addTickets(
      [{ id: 'COD-1', kind: 'ticket' }],
      async () => {
        throw 'boom'
      },
      () => {},
    )
    await expect(run).rejects.toThrow('COD-1: boom')
  })

  it('resolves without enqueueing for an empty plan', async () => {
    let calls = 0
    await addTickets(
      [],
      async () => {
        calls++
        return queueResponse()
      },
      () => {},
    )
    expect(calls).toBe(0)
  })
})
