import { describe, expect, it } from 'vitest'

import { notificationTarget, type HubNotification } from './notification-center'

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
