import { describe, expect, it } from 'vitest'

import type { QueueItem, QueueResponse } from '@/lib/queue'

import { NAV_GROUPS } from './nav-items'
import { navBadge } from './sidebar'

const loopItem = NAV_GROUPS.flatMap((group) => group.items).find(
  (item) => item.loop,
)

function item(status: string, id = 'LOOP-1'): QueueItem {
  return {
    position: 1,
    kind: 'ticket',
    id,
    status,
  }
}

function queue(items: QueueItem[], draining = false): QueueResponse {
  return {
    repo: 'loop',
    draining,
    shutting_down: false,
    items,
  }
}

describe('Loop sidebar badge', () => {
  it('counts only items the queue can still act on', () => {
    const badge = navBadge(
      loopItem!,
      0,
      { total: 0, awaiting: 0 },
      queue([item('pending'), item('paused', 'LOOP-2'), item('done', 'LOOP-3')]),
    )

    expect(badge).toEqual({
      count: 2,
      tone: 'muted',
      title: '2 items in queue',
    })
  })

  it('marks a draining queue as running', () => {
    const badge = navBadge(
      loopItem!,
      0,
      { total: 0, awaiting: 0 },
      queue([item('pending')], true),
    )

    expect(badge).toEqual({
      count: 1,
      tone: 'active',
      title: '1 item in queue — running',
    })
  })

  it('marks a standalone running item as running', () => {
    const badge = navBadge(
      loopItem!,
      0,
      { total: 0, awaiting: 0 },
      queue([item('running')]),
    )

    expect(badge?.tone).toBe('active')
  })

  it('hides the badge when no actionable items remain', () => {
    expect(
      navBadge(
        loopItem!,
        0,
        { total: 0, awaiting: 0 },
        queue([item('done'), item('failed', 'LOOP-2')]),
      ),
    ).toBeNull()
  })
})
