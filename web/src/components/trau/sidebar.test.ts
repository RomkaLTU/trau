import { describe, expect, it } from 'vitest'

import type { QueueItem, QueueResponse } from '@/lib/queue'

import { NAV_GROUPS } from './nav-items'
import { navBadge } from './sidebar'

const navItems = NAV_GROUPS.flatMap((group) => group.items)
const loopItem = navItems.find((item) => item.loop)
const inboxItem = navItems.find((item) => item.inbox)
const researchItem = navItems.find((item) => item.research)

const idle = { interviews: 0, research: 0 }

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
    stopping: false,
    items,
  }
}

describe('Loop sidebar badge', () => {
  it('counts only items the queue can still act on', () => {
    const badge = navBadge(
      loopItem!,
      0,
      { total: 0, awaiting: 0 },
      idle,
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
      idle,
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
      idle,
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
        idle,
        queue([item('done'), item('failed', 'LOOP-2')]),
      ),
    ).toBeNull()
  })
})

describe('Research sidebar badge', () => {
  it('lights while research sessions work', () => {
    const badge = navBadge(
      researchItem!,
      0,
      { total: 0, awaiting: 0 },
      { interviews: 0, research: 2 },
    )

    expect(badge).toEqual({
      count: 2,
      tone: 'active',
      title: '2 research sessions in progress',
    })
  })

  it('names a lone running session in the singular', () => {
    const badge = navBadge(
      researchItem!,
      0,
      { total: 0, awaiting: 0 },
      { interviews: 0, research: 1 },
    )

    expect(badge?.title).toBe('1 research session in progress')
  })

  it('stays silent when no research session is running', () => {
    expect(
      navBadge(researchItem!, 0, { total: 0, awaiting: 0 }, idle),
    ).toBeNull()
  })
})

describe('Inbox sidebar badge', () => {
  it('lights while an interview works, keeping the triage total', () => {
    const badge = navBadge(
      inboxItem!,
      0,
      { total: 3, awaiting: 0 },
      { interviews: 1, research: 0 },
    )

    expect(badge).toEqual({
      count: 3,
      tone: 'active',
      title: '1 interview in progress',
    })
  })

  it('falls back to the running count when nothing is queued for triage', () => {
    const badge = navBadge(
      inboxItem!,
      0,
      { total: 0, awaiting: 0 },
      { interviews: 2, research: 0 },
    )

    expect(badge).toEqual({
      count: 2,
      tone: 'active',
      title: '2 interviews in progress',
    })
  })

  it('lets a waiting question outrank a working interview', () => {
    const badge = navBadge(
      inboxItem!,
      0,
      { total: 3, awaiting: 1 },
      { interviews: 1, research: 0 },
    )

    expect(badge).toEqual({
      count: 1,
      tone: 'warn',
      title: '1 question waiting on you',
    })
  })

  it('keeps the muted triage badge when nothing is running', () => {
    expect(navBadge(inboxItem!, 0, { total: 2, awaiting: 0 }, idle)).toEqual({
      count: 2,
      tone: 'muted',
      title: '2 issues to triage',
    })
  })
})
