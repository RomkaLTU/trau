import { useEffect, useRef } from 'react'
import { queryOptions, useQueryClient } from '@tanstack/react-query'

import { apiFetch } from './api'
import { subscribeAllEvents, type RepoFeedEvent } from './events'
import { draftItemId } from './inbox'

export type NotificationKind =
  | 'grill_question'
  | 'run_paused'
  | 'run_faulted'
  | 'run_quarantined'

// HubNotification mirrors the hub's notifications resource — named apart from the
// browser Notification the OS layer raises (@/lib/notifications). Ref is the grill
// session id or the run's ticket; ReadAt is empty while unread.
export interface HubNotification {
  id: number
  repo: string
  kind: NotificationKind
  ref: string
  issue_id?: string
  title: string
  body: string
  created_at: string
  updated_at: string
  read_at?: string
}

export interface NotificationsResponse {
  notifications: HubNotification[]
  unread_count: number
}

export const notificationsQueryKey = ['notifications'] as const

const NOTIFICATION_KIND = 'notification'

async function fetchNotifications(): Promise<NotificationsResponse> {
  const res = await apiFetch('/api/v1/notifications')
  if (!res.ok) {
    throw new Error(`notifications request failed: ${res.status}`)
  }
  return res.json()
}

// notificationsQueryOptions reads the recent notifications and unread count. It has
// no refetchInterval — the SSE wake-up frame drives freshness (see
// useNotificationEvents).
export const notificationsQueryOptions = () =>
  queryOptions({
    queryKey: notificationsQueryKey,
    queryFn: fetchNotifications,
  })

// useNotificationEvents taps the shared machine-wide stream for the hub's live
// wake-up frames — id-less, so never part of a reconnect backfill — refetching the
// notifications query and handing each frame's notification to onNotification with
// its repo label. Shared with the notification center.
export function useNotificationEvents(
  onNotification: (notification: HubNotification, repo: string) => void,
): void {
  const queryClient = useQueryClient()
  const callback = useRef(onNotification)
  callback.current = onNotification

  useEffect(() => {
    return subscribeAllEvents((ev) => {
      const notification = notificationOf(ev)
      if (!notification) return
      void queryClient.invalidateQueries({ queryKey: notificationsQueryKey })
      callback.current(notification, ev.repo)
    }, () => {})
  }, [queryClient])
}

function notificationOf(ev: RepoFeedEvent): HubNotification | null {
  if (ev.kind !== NOTIFICATION_KIND) return null
  const notification = ev.fields?.notification as HubNotification | undefined
  return notification ?? null
}

export type NotificationTarget =
  | { kind: 'inbox'; issue: string }
  | { kind: 'run'; repo: string; ticket: string }
  | null

// notificationTarget is where clicking a notification lands: a grill question at
// its inbox item — an issue-less authoring session addressed by its draft row —
// and a run at its detail page.
export function notificationTarget(
  notification: HubNotification,
  repo: string,
): NotificationTarget {
  switch (notification.kind) {
    case 'grill_question':
      return {
        kind: 'inbox',
        issue: notification.issue_id || draftItemId(notification.ref),
      }
    case 'run_paused':
    case 'run_faulted':
    case 'run_quarantined':
      return { kind: 'run', repo, ticket: notification.ref }
    default:
      return null
  }
}
