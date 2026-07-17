import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import {
  Bell,
  CheckCheck,
  MessageCircleQuestion,
  TriangleAlert,
  type LucideIcon,
} from 'lucide-react'

import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  markAllNotificationsRead,
  markAllReadInResponse,
  markNotificationRead,
  markReadInResponse,
  navigateToNotification,
  notificationTarget,
  notificationsQueryKey,
  notificationsQueryOptions,
  sortByNewest,
  unreadBadgeLabel,
  type HubNotification,
  type NotificationKind,
  type NotificationsResponse,
} from '@/lib/notification-center'
import { cn } from '@/lib/utils'

const KIND_ICON: Record<NotificationKind, LucideIcon> = {
  grill_question: MessageCircleQuestion,
  run_paused: TriangleAlert,
  run_faulted: TriangleAlert,
  run_quarantined: TriangleAlert,
}

export function NotificationBell() {
  const [open, setOpen] = useState(false)
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { data } = useQuery(notificationsQueryOptions())

  const unreadCount = data?.unread_count ?? 0
  const unreadLabel = unreadBadgeLabel(unreadCount)
  const notifications = data ? sortByNewest(data.notifications) : []

  function patchCache(next: (prev: NotificationsResponse) => NotificationsResponse) {
    const prev = queryClient.getQueryData<NotificationsResponse>(
      notificationsQueryKey,
    )
    if (prev) queryClient.setQueryData(notificationsQueryKey, next(prev))
    return { prev }
  }

  function restoreCache(ctx: { prev?: NotificationsResponse } | undefined) {
    if (ctx?.prev) queryClient.setQueryData(notificationsQueryKey, ctx.prev)
  }

  const refetch = () =>
    void queryClient.invalidateQueries({ queryKey: notificationsQueryKey })

  const markRead = useMutation({
    mutationFn: (id: number) => markNotificationRead(id),
    onMutate: (id) =>
      patchCache((prev) => markReadInResponse(prev, id, new Date().toISOString())),
    onError: (_err, _id, ctx) => restoreCache(ctx),
    onSettled: refetch,
  })

  const markAll = useMutation({
    mutationFn: () => markAllNotificationsRead(),
    onMutate: () =>
      patchCache((prev) => markAllReadInResponse(prev, new Date().toISOString())),
    onError: (_err, _vars, ctx) => restoreCache(ctx),
    onSettled: refetch,
  })

  function openNotification(notification: HubNotification) {
    if (!notification.read_at) markRead.mutate(notification.id)
    setOpen(false)
    navigateToNotification(
      navigate,
      notificationTarget(notification, notification.repo),
    )
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        title="Notifications"
        aria-label={
          unreadCount > 0 ? `Notifications, ${unreadCount} unread` : 'Notifications'
        }
        className="relative rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
      >
        <Bell className="size-4" aria-hidden="true" />
        {unreadLabel ? (
          <span className="absolute -right-1 -top-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full border border-warn/50 bg-warn/12 px-1 font-mono text-[0.6rem] leading-none text-warn">
            {unreadLabel}
          </span>
        ) : null}
      </PopoverTrigger>
      <PopoverContent align="start" sideOffset={8} className="w-80 p-0">
        <div className="flex items-center justify-between border-b border-border px-3 py-2">
          <span className="font-mono text-[0.65rem] uppercase tracking-[0.2em] text-muted-foreground">
            Notifications
          </span>
          <button
            type="button"
            onClick={() => markAll.mutate()}
            disabled={unreadCount === 0 || markAll.isPending}
            className="inline-flex items-center gap-1 font-mono text-[0.7rem] text-muted-foreground transition-colors hover:text-foreground disabled:cursor-default disabled:opacity-40"
          >
            <CheckCheck className="size-3" aria-hidden="true" />
            Mark all read
          </button>
        </div>

        {notifications.length === 0 ? (
          <div className="flex flex-col items-center gap-1.5 px-3 py-10 text-center">
            <Bell className="size-5 text-muted-foreground/50" aria-hidden="true" />
            <p className="font-mono text-sm text-muted-foreground">
              Nothing needs you
            </p>
          </div>
        ) : (
          <ul className="max-h-96 overflow-y-auto py-1">
            {notifications.map((notification) => (
              <li key={notification.id}>
                <NotificationRow
                  notification={notification}
                  onOpen={() => openNotification(notification)}
                />
              </li>
            ))}
          </ul>
        )}
      </PopoverContent>
    </Popover>
  )
}

function NotificationRow({
  notification,
  onOpen,
}: {
  notification: HubNotification
  onOpen: () => void
}) {
  const Icon = KIND_ICON[notification.kind]
  const unread = !notification.read_at

  return (
    <button
      type="button"
      onClick={onOpen}
      className={cn(
        'flex w-full items-start gap-2.5 px-3 py-2 text-left transition-colors hover:bg-secondary',
        unread && 'bg-primary/5',
      )}
    >
      <Icon
        className={cn(
          'mt-0.5 size-4 shrink-0',
          notification.kind === 'grill_question' ? 'text-primary' : 'text-warn',
          !unread && 'opacity-50',
        )}
        aria-hidden="true"
      />
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-1.5">
          <span
            className={cn(
              'flex-1 truncate text-sm',
              unread ? 'font-medium text-foreground' : 'text-muted-foreground',
            )}
          >
            {notification.title}
          </span>
          {unread ? (
            <span
              aria-hidden="true"
              className="size-1.5 shrink-0 rounded-full bg-primary"
            />
          ) : null}
        </span>
        <span className="mt-0.5 flex items-center gap-1.5 font-mono text-[0.7rem] text-muted-foreground">
          <span className="truncate">{notification.repo}</span>
          <span aria-hidden="true">·</span>
          <span className="shrink-0">{relativeTime(notification.updated_at)}</span>
        </span>
      </span>
    </button>
  )
}

function relativeTime(iso: string, now: number = Date.now()): string {
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return ''
  const secs = Math.max(0, Math.round((now - then) / 1000))
  if (secs < 60) return 'just now'
  const mins = Math.round(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.round(hours / 24)}d ago`
}
