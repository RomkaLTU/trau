import { describe, expect, it } from 'vitest'

import {
  addByIdState,
  pendingBehind,
  runNextCopy,
  statusWarning,
} from './add-by-id'
import { IssueFetchError, type Issue } from './issues'
import type { QueueItem } from './queue'

function issue(over: Partial<Issue> = {}): Issue {
  return {
    repo: 'trau',
    provider: 'linear',
    id: 'COD-1',
    title: 'Ticket',
    description: '',
    status: 'Todo',
    group: 'unstarted',
    labels: [],
    ready: true,
    has_children: false,
    comments: [],
    in_project: true,
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

describe('statusWarning', () => {
  it('warns on done, canceled, and started tickets', () => {
    for (const group of ['done', 'canceled', 'started']) {
      expect(statusWarning(issue({ group }))?.tone).toBe('warn')
    }
  })

  it('notes a ready-but-unlabeled ticket without blocking', () => {
    expect(statusWarning(issue({ ready: false }))?.tone).toBe('info')
  })

  it('stays silent for a ready unstarted ticket', () => {
    expect(statusWarning(issue())).toBeNull()
  })
})

describe('addByIdState', () => {
  it('confirms a fetched in-project ticket', () => {
    expect(addByIdState('COD-1', issue(), null)).toEqual({
      confirmed: true,
      wrongProject: false,
      confirmless: false,
      canQueue: true,
    })
  })

  it('blocks a fetched cross-project ticket', () => {
    const state = addByIdState('COD-1', issue({ in_project: false }), null)
    expect(state.wrongProject).toBe(true)
    expect(state.canQueue).toBe(false)
  })

  it('allows the confirmless path when the repo has no tracker reader', () => {
    const state = addByIdState(
      'COD-1',
      undefined,
      new IssueFetchError('no-tracker', 'no tracker'),
    )
    expect(state.confirmless).toBe(true)
    expect(state.canQueue).toBe(true)
  })

  it('keeps a not-found id blocked', () => {
    const state = addByIdState(
      'COD-9',
      undefined,
      new IssueFetchError('not-found', 'missing'),
    )
    expect(state.canQueue).toBe(false)
  })

  it('reports nothing before an id is submitted', () => {
    expect(addByIdState('', undefined, null).canQueue).toBe(false)
  })
})

describe('pendingBehind', () => {
  it('counts unsettled items excluding the ticket itself', () => {
    const items = [
      item({ id: 'COD-1' }),
      item({ id: 'COD-2' }),
      item({ id: 'COD-3', status: 'done' }),
      item({ id: 'COD-4', status: 'paused' }),
    ]
    expect(pendingBehind(items, 'COD-1')).toBe(2)
    expect(pendingBehind(items, 'COD-9')).toBe(3)
  })

  it('is zero for an empty queue', () => {
    expect(pendingBehind([], 'COD-1')).toBe(0)
  })
})

describe('runNextCopy', () => {
  it('names the ticket and the queue behind it', () => {
    expect(runNextCopy('COD-1', 0)).toBe('Runs COD-1 next.')
    expect(runNextCopy('COD-1', 1)).toBe(
      'Runs COD-1 next, then 1 more queued item.',
    )
    expect(runNextCopy('COD-1', 3)).toBe(
      'Runs COD-1 next, then 3 more queued items.',
    )
  })
})
