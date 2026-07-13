import { describe, expect, it } from 'vitest'

import {
  addAllLabel,
  groupEligible,
  planAddAll,
  type EligibleTicket,
} from './eligible'
import type { QueueItem } from './queue'

function ticket(over: Partial<EligibleTicket>): EligibleTicket {
  return {
    id: 'COD-1',
    title: 'a ticket',
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

describe('planAddAll', () => {
  it('is empty for an empty eligible list', () => {
    expect(planAddAll([], [])).toEqual({ items: [], epics: 0, tickets: 0 })
  })

  it('groups sub-issues under their epics and keeps standalones as tickets', () => {
    const plan = planAddAll(
      [
        ticket({ id: 'COD-2', parent: 'COD-100' }),
        ticket({ id: 'COD-3', parent: 'COD-100' }),
        ticket({ id: 'COD-4' }),
        ticket({ id: 'COD-5', parent: 'COD-200' }),
      ],
      [],
    )
    expect(plan).toEqual({
      items: [
        { id: 'COD-100', kind: 'epic' },
        { id: 'COD-4', kind: 'ticket' },
        { id: 'COD-200', kind: 'epic' },
      ],
      epics: 2,
      tickets: 1,
    })
  })

  it('enqueues a parentless epic as an epic item', () => {
    const plan = planAddAll([ticket({ id: 'COD-9', has_children: true })], [])
    expect(plan.items).toEqual([{ id: 'COD-9', kind: 'epic' }])
    expect(plan.epics).toBe(1)
  })

  it('dedupes an epic that is both eligible itself and the parent of an eligible child', () => {
    const plan = planAddAll(
      [
        ticket({ id: 'COD-10', has_children: true }),
        ticket({ id: 'COD-11', parent: 'COD-10' }),
      ],
      [],
    )
    expect(plan.items).toEqual([{ id: 'COD-10', kind: 'epic' }])
  })

  it('groups a nested epic under its immediate parent', () => {
    const plan = planAddAll(
      [ticket({ id: 'COD-12', parent: 'COD-1', has_children: true })],
      [],
    )
    expect(plan.items).toEqual([{ id: 'COD-1', kind: 'epic' }])
  })

  it('skips ids already in the queue', () => {
    const plan = planAddAll(
      [ticket({ id: 'COD-4' }), ticket({ id: 'COD-5' })],
      [item({ id: 'COD-4' })],
    )
    expect(plan.items).toEqual([{ id: 'COD-5', kind: 'ticket' }])
  })

  it('skips a sub-issue whose epic is already queued', () => {
    const plan = planAddAll(
      [ticket({ id: 'COD-2', parent: 'COD-100' })],
      [item({ id: 'COD-100', kind: 'epic' })],
    )
    expect(plan.items).toEqual([])
  })

  it('skips a ticket covered by a queued epic sub-issue', () => {
    const plan = planAddAll(
      [ticket({ id: 'COD-3' })],
      [
        item({
          id: 'COD-100',
          kind: 'epic',
          sub_issues: [{ id: 'COD-3', title: 'x', state: 'todo' }],
        }),
      ],
    )
    expect(plan.items).toEqual([])
  })
})

describe('groupEligible', () => {
  it('leaves a parentless list as standalone ticket rows in order', () => {
    const rows = groupEligible([
      ticket({ id: 'COD-1' }),
      ticket({ id: 'COD-2', has_children: true }),
    ])
    expect(rows).toEqual([
      { kind: 'ticket', ticket: ticket({ id: 'COD-1' }) },
      { kind: 'ticket', ticket: ticket({ id: 'COD-2', has_children: true }) },
    ])
  })

  it('nests sub-issues under their epic and keeps top-level tickets flat', () => {
    const epic = ticket({ id: 'COD-100', has_children: true })
    const a = ticket({ id: 'COD-101', parent: 'COD-100' })
    const b = ticket({ id: 'COD-102', parent: 'COD-100' })
    const solo = ticket({ id: 'COD-4' })
    const rows = groupEligible([epic, a, b, solo])
    expect(rows).toEqual([
      { kind: 'epic', epicId: 'COD-100', epic, children: [a, b] },
      { kind: 'ticket', ticket: solo },
    ])
  })

  it('heads a group with a bare epic id when the epic itself is not eligible', () => {
    const child = ticket({ id: 'COD-201', parent: 'COD-200' })
    const rows = groupEligible([child])
    expect(rows).toEqual([
      { kind: 'epic', epicId: 'COD-200', epic: null, children: [child] },
    ])
  })

  it('emits the epic once when a child precedes its epic in the list', () => {
    const child = ticket({ id: 'COD-11', parent: 'COD-10' })
    const epic = ticket({ id: 'COD-10', has_children: true })
    const rows = groupEligible([child, epic])
    expect(rows).toEqual([
      { kind: 'epic', epicId: 'COD-10', epic, children: [child] },
    ])
  })
})

describe('addAllLabel', () => {
  it('names both groups with pluralization', () => {
    expect(addAllLabel({ items: [], epics: 2, tickets: 3 })).toBe(
      'Add all eligible (2 epics + 3 tickets)',
    )
  })

  it('uses the singular for a lone epic or ticket', () => {
    expect(addAllLabel({ items: [], epics: 1, tickets: 1 })).toBe(
      'Add all eligible (1 epic + 1 ticket)',
    )
  })

  it('drops an empty group', () => {
    expect(addAllLabel({ items: [], epics: 0, tickets: 3 })).toBe(
      'Add all eligible (3 tickets)',
    )
    expect(addAllLabel({ items: [], epics: 2, tickets: 0 })).toBe(
      'Add all eligible (2 epics)',
    )
  })
})
