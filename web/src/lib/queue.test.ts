import { describe, expect, it } from 'vitest'

import {
  queueCounts,
  queueExecutable,
  skipResumeApplies,
  type QueueItem,
} from './queue'
import type { Run } from './runs'

function item(over: Partial<QueueItem>): QueueItem {
  return {
    position: 1,
    kind: 'ticket',
    id: 'COD-1',
    status: 'pending',
    ...over,
  }
}

function run(over: Partial<Run>): Run {
  return {
    ticket: 'COD-1',
    phase: 'implement',
    phase_rank: 0,
    terminal: false,
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

describe('queueExecutable', () => {
  it('counts each ticket once', () => {
    expect(
      queueExecutable([
        item({ id: 'COD-1', kind: 'ticket' }),
        item({ id: 'COD-2', kind: 'ticket' }),
      ]),
    ).toBe(2)
  })

  it('counts an epic by its not-done sub-issues', () => {
    expect(
      queueExecutable([
        item({ id: 'COD-1', kind: 'ticket' }),
        item({
          id: 'COD-2',
          kind: 'epic',
          sub_issues: [
            { id: 'COD-3', title: 'a', state: 'todo' },
            { id: 'COD-4', title: 'b', state: 'done' },
            { id: 'COD-5', title: 'c', state: 'todo' },
          ],
        }),
      ]),
    ).toBe(3)
  })
})

describe('skipResumeApplies', () => {
  it('is false for an all-pending queue with no runs', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' }), item({ id: 'COD-2' })],
        [],
      ),
    ).toBe(false)
  })

  it.each(['done', 'failed', 'skipped', 'paused', 'running'])(
    'is true when an item has %s status',
    (status) => {
      expect(
        skipResumeApplies(
          [item({ id: 'COD-1' }), item({ id: 'COD-2', status })],
          [],
        ),
      ).toBe(true)
    },
  )

  it('is true when a non-terminal run matches a queued ticket id', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' })],
        [run({ ticket: 'COD-1', terminal: false })],
      ),
    ).toBe(true)
  })

  it('is true when a non-terminal run matches an epic sub-issue id', () => {
    expect(
      skipResumeApplies(
        [
          item({
            id: 'COD-2',
            kind: 'epic',
            sub_issues: [{ id: 'COD-3', title: 'a', state: 'todo' }],
          }),
        ],
        [run({ ticket: 'COD-3', terminal: false })],
      ),
    ).toBe(true)
  })

  it('is false when the only matching run is terminal', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' })],
        [run({ ticket: 'COD-1', terminal: true })],
      ),
    ).toBe(false)
  })

  it('is false when a non-terminal run is for an unrelated ticket', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' })],
        [run({ ticket: 'COD-99', terminal: false })],
      ),
    ).toBe(false)
  })
})
