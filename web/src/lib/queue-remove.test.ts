import { describe, expect, it } from 'vitest'

import {
  removableFromQueue,
  removeFromQueueTitle,
  removeFromQueueWarning,
  REMOVE_FROM_QUEUE_LABEL,
} from './queue-remove'
import type { QueueItem } from './queue'

function item(over: Partial<QueueItem> = {}): QueueItem {
  return { position: 1, kind: 'ticket', id: 'COD-1229', status: 'pending', ...over }
}

describe('removableFromQueue', () => {
  it('refuses a running row, which is stopped first and removed once parked', () => {
    expect(removableFromQueue(item({ status: 'running' }))).toBe(false)
  })

  it('allows every row the queue is not running', () => {
    for (const status of ['pending', 'paused', 'failed', 'done']) {
      expect(removableFromQueue(item({ status }))).toBe(true)
    }
  })
})

describe('removeFromQueueTitle', () => {
  it('asks to remove the item', () => {
    expect(removeFromQueueTitle(item())).toBe('Remove COD-1229 from the queue?')
  })
})

describe('removeFromQueueWarning', () => {
  it('names the wipe and never mentions a stop', () => {
    const warning = removeFromQueueWarning(item())
    expect(warning).toBe(
      'The row goes, its saved progress is wiped and the ticket goes back to Ready — a later pickup starts a brand-new run.',
    )
    expect(warning).not.toContain('stops')
  })

  it('counts the sub-issues an epic takes with it, pluralized', () => {
    const sub = (id: string) => ({ id, title: id, state: 'todo' })
    expect(
      removeFromQueueWarning(
        item({ kind: 'epic', sub_issues: [sub('COD-1'), sub('COD-2')] }),
      ),
    ).toContain('Its 2 sub-issues leave the queue with it.')
    expect(
      removeFromQueueWarning(item({ kind: 'epic', sub_issues: [sub('COD-1')] })),
    ).toContain('Its 1 sub-issue leaves the queue with it.')
  })

  it('never mentions sub-issues on a plain ticket', () => {
    expect(removeFromQueueWarning(item())).not.toContain('sub-issue')
  })
})

describe('REMOVE_FROM_QUEUE_LABEL', () => {
  it('says what the confirm actually does', () => {
    expect(REMOVE_FROM_QUEUE_LABEL).toBe('Remove from queue')
  })
})
