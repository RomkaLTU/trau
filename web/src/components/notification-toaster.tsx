import { useRef } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { fireNotification, useNotifications } from '@/lib/notifications'
import {
  notificationTarget,
  useNotificationEvents,
  type NotificationTarget,
} from '@/lib/notification-center'
import { isConversationOpen } from '@/lib/open-conversation'

// NotificationToaster is the headless bridge from the hub's live needs-attention
// frames to a toast — and, when the tab is hidden, an OS notification. It renders
// nothing; the toasts land in the root <Toaster />.
export function NotificationToaster() {
  const navigate = useNavigate()
  const { enabled } = useNotifications()
  const enabledRef = useRef(enabled)
  enabledRef.current = enabled

  useNotificationEvents((notification, repo) => {
    if (
      notification.kind === 'grill_question' &&
      isConversationOpen(notification.ref)
    ) {
      return
    }

    const target = notificationTarget(notification, repo)
    toast.custom((id) => (
      <NotificationCard
        title={notification.title}
        repo={repo}
        body={notification.body}
        onOpen={() => {
          toast.dismiss(id)
          go(navigate, target)
        }}
      />
    ))

    if (document.hidden && enabledRef.current) {
      fireNotification(
        notification.title,
        notification.body,
        notification.kind + notification.ref,
      )
    }
  })

  return null
}

function go(navigate: ReturnType<typeof useNavigate>, target: NotificationTarget) {
  if (!target) return
  if (target.kind === 'inbox') {
    void navigate({ to: '/inbox', search: { issue: target.issue } })
    return
  }
  void navigate({
    to: '/runs/$repo/$ticket',
    params: { repo: target.repo, ticket: target.ticket },
  })
}

function NotificationCard({
  title,
  repo,
  body,
  onOpen,
}: {
  title: string
  repo: string
  body: string
  onOpen: () => void
}) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex w-full flex-col gap-1.5 rounded-lg border border-border bg-popover px-4 py-3 text-left shadow-lg transition-colors hover:bg-accent"
    >
      <div className="flex items-center gap-2">
        <Badge variant="outline" className="font-mono text-[10px]">
          {repo}
        </Badge>
        <span className="min-w-0 flex-1 truncate text-sm font-medium text-popover-foreground">
          {title}
        </span>
      </div>
      {body && (
        <p className="line-clamp-2 text-xs leading-relaxed text-muted-foreground">
          {body}
        </p>
      )}
    </button>
  )
}
