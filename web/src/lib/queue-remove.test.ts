import { describe, expect, it } from 'vitest'

import {
  removeFromQueueLabel,
  removeFromQueueTitle,
  removeFromQueueWarning,
} from './queue-remove'
import type { QueueItem } from './queue'

function item(over: Partial<QueueItem> = {}): QueueItem {
  return { position: 1, kind: 'ticket', id: 'COD-1229', status: 'pending', ...over }
}

describe('removeFromQueueTitle', () => {
  it('asks to remove a pending item', () => {
    expect(removeFromQueueTitle(item())).toBe('Remove COD-1229 from the queue?')
  })

  it('names the stop a running item needs first', () => {
    expect(removeFromQueueTitle(item({ status: 'running' }))).toBe(
      'Stop COD-1229 and remove it from the queue?',
    )
  })
})

describe('removeFromQueueWarning', () => {
  it('names the wipe and never mentions a stop for a pending item', () => {
    const warning = removeFromQueueWarning(item())
    expect(warning).toBe(
      'The row goes, its saved progress is wiped and the ticket goes back to Ready — a later pickup starts a brand-new run.',
    )
    expect(warning).not.toContain('stops')
  })

  it('leads with the stop for a running item, still naming the wipe', () => {
    const warning = removeFromQueueWarning(item({ status: 'running' }))
    expect(warning).toMatch(/^The run stops first/)
    expect(warning).toContain('the ticket goes back to Ready')
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

describe('removeFromQueueLabel', () => {
  it('says what the confirm actually does', () => {
    expect(removeFromQueueLabel(item())).toBe('Remove from queue')
    expect(removeFromQueueLabel(item({ status: 'running' }))).toBe(
      'Stop and remove',
    )
  })
})
