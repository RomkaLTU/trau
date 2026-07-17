import { describe, expect, it } from 'vitest'

import {
  markAllReadInResponse,
  markReadInResponse,
  notificationTarget,
  sortByNewest,
  unreadBadgeLabel,
  type HubNotification,
  type NotificationsResponse,
} from './notification-center'

function notif(overrides: Partial<HubNotification>): HubNotification {
  return {
    id: 1,
    repo: '/repo',
    kind: 'grill_question',
    ref: '42',
    title: 'title',
    body: 'body',
    created_at: '',
    updated_at: '',
    ...overrides,
  }
}

describe('notificationTarget', () => {
  it('sends a grill question to its inbox issue', () => {
    const target = notificationTarget(
      notif({ kind: 'grill_question', issue_id: 'COD-1', ref: '42' }),
      'app',
    )
    expect(target).toEqual({ kind: 'inbox', issue: 'COD-1' })
  })

  it('addresses an issue-less grill question by its draft row', () => {
    const target = notificationTarget(
      notif({ kind: 'grill_question', issue_id: '', ref: '42' }),
      'app',
    )
    expect(target).toEqual({ kind: 'inbox', issue: 'draft:42' })
  })

  it.each(['run_paused', 'run_faulted', 'run_quarantined'] as const)(
    'sends %s to its run detail page with repo and ticket',
    (kind) => {
      const target = notificationTarget(notif({ kind, ref: 'COD-9' }), 'app')
      expect(target).toEqual({ kind: 'run', repo: 'app', ticket: 'COD-9' })
    },
  )
})

describe('unreadBadgeLabel', () => {
  it('hides at zero', () => {
    expect(unreadBadgeLabel(0)).toBeNull()
    expect(unreadBadgeLabel(-1)).toBeNull()
  })

  it('shows the exact count through nine', () => {
    expect(unreadBadgeLabel(1)).toBe('1')
    expect(unreadBadgeLabel(9)).toBe('9')
  })

  it('overflows past nine as 9+', () => {
    expect(unreadBadgeLabel(10)).toBe('9+')
    expect(unreadBadgeLabel(128)).toBe('9+')
  })
})

describe('sortByNewest', () => {
  it('orders by updated_at descending without mutating the input', () => {
    const input = [
      notif({ id: 1, updated_at: '2026-07-17T10:00:00Z' }),
      notif({ id: 2, updated_at: '2026-07-17T12:00:00Z' }),
      notif({ id: 3, updated_at: '2026-07-17T11:00:00Z' }),
    ]
    expect(sortByNewest(input).map((n) => n.id)).toEqual([2, 3, 1])
    expect(input.map((n) => n.id)).toEqual([1, 2, 3])
  })
})

function resp(overrides: Partial<NotificationsResponse>): NotificationsResponse {
  return { notifications: [], unread_count: 0, ...overrides }
}

describe('markReadInResponse', () => {
  it('settles one row and decrements the unread count', () => {
    const before = resp({
      notifications: [
        notif({ id: 1, read_at: undefined }),
        notif({ id: 2, read_at: undefined }),
      ],
      unread_count: 5,
    })
    const after = markReadInResponse(before, 1, '2026-07-17T12:00:00Z')
    expect(after.notifications[0].read_at).toBe('2026-07-17T12:00:00Z')
    expect(after.notifications[1].read_at).toBeUndefined()
    expect(after.unread_count).toBe(4)
  })

  it('leaves an already-read row and its count untouched', () => {
    const before = resp({
      notifications: [notif({ id: 1, read_at: '2026-07-17T09:00:00Z' })],
      unread_count: 3,
    })
    const after = markReadInResponse(before, 1, '2026-07-17T12:00:00Z')
    expect(after).toBe(before)
  })

  it('never drives the count below zero', () => {
    const before = resp({
      notifications: [notif({ id: 1, read_at: undefined })],
      unread_count: 0,
    })
    expect(markReadInResponse(before, 1, 'now').unread_count).toBe(0)
  })
})

describe('markAllReadInResponse', () => {
  it('settles every unread row and zeroes the count', () => {
    const before = resp({
      notifications: [
        notif({ id: 1, read_at: undefined }),
        notif({ id: 2, read_at: '2026-07-17T09:00:00Z' }),
      ],
      unread_count: 7,
    })
    const after = markAllReadInResponse(before, '2026-07-17T12:00:00Z')
    expect(after.notifications[0].read_at).toBe('2026-07-17T12:00:00Z')
    expect(after.notifications[1].read_at).toBe('2026-07-17T09:00:00Z')
    expect(after.unread_count).toBe(0)
  })
})
