import {
  Ban,
  Bell,
  BellOff,
  CirclePause,
  GitMerge,
  TriangleAlert,
  X,
  type LucideIcon,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { useAwayMonitor } from '@/lib/away'
import { useNotifications, type Notifications } from '@/lib/notifications'
import {
  RECAP_CATEGORIES,
  describeRecapItem,
  type RecapCategory,
  type RecapItem,
} from '@/lib/recap'

interface CategoryMeta {
  label: string
  icon: LucideIcon
  text: string
}

const CATEGORY_META: Record<RecapCategory, CategoryMeta> = {
  merged: { label: 'Merged', icon: GitMerge, text: 'text-emerald-600 dark:text-emerald-400' },
  paused: { label: 'Paused', icon: CirclePause, text: 'text-amber-600 dark:text-amber-400' },
  faulted: { label: 'Faulted', icon: TriangleAlert, text: 'text-destructive' },
  quarantined: { label: 'Quarantined', icon: Ban, text: 'text-orange-600 dark:text-orange-400' },
}

export function AwayRecap() {
  const notifications = useNotifications()
  const { recap, dismiss } = useAwayMonitor(notifications.enabled)

  if (recap.total === 0) {
    return <NotificationsPrompt notifications={notifications} />
  }

  const noun = recap.total === 1 ? 'change' : 'changes'
  return (
    <section className="mb-6 rounded-lg border bg-card">
      <header className="flex items-center gap-3 border-b px-4 py-3">
        <h2 className="text-sm font-medium">Since you were away</h2>
        <span className="text-xs text-muted-foreground tabular-nums">
          {recap.total} {noun}
        </span>
        <div className="ml-auto flex items-center gap-1">
          <NotificationToggle notifications={notifications} />
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss recap"
            className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
          >
            <X className="size-4" />
          </button>
        </div>
      </header>
      <div className="divide-y">
        {RECAP_CATEGORIES.map((cat) => (
          <RecapGroup key={cat} category={cat} items={recap[cat]} />
        ))}
      </div>
    </section>
  )
}

function RecapGroup({
  category,
  items,
}: {
  category: RecapCategory
  items: RecapItem[]
}) {
  if (items.length === 0) return null
  const { label, icon: Icon, text } = CATEGORY_META[category]
  return (
    <div className="px-4 py-3">
      <div className={cn('flex items-center gap-2 text-sm font-medium', text)}>
        <Icon className="size-4" />
        {label}
        <span className="text-xs font-normal text-muted-foreground tabular-nums">
          {items.length}
        </span>
      </div>
      <ul className="mt-2 flex flex-col gap-1.5">
        {items.map((item) => (
          <li key={item.key} className="flex items-center gap-2 text-sm">
            <Badge variant="outline" className="font-mono text-[10px]">
              {item.repo}
            </Badge>
            <span className="min-w-0 truncate text-muted-foreground">
              {describeRecapItem(item)}
            </span>
            <span className="ml-auto shrink-0 font-mono text-xs tabular-nums text-muted-foreground">
              {ago(item.ts)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function NotificationsPrompt({ notifications }: { notifications: Notifications }) {
  if (
    !notifications.supported ||
    notifications.permission === 'denied' ||
    notifications.enabled
  ) {
    return null
  }
  return (
    <div className="mb-6 flex items-center gap-3 rounded-lg border bg-card px-4 py-2.5 text-sm">
      <Bell className="size-4 shrink-0 text-muted-foreground" />
      <span className="text-muted-foreground">
        Get notified when a run pauses, faults, quarantines, or merges.
      </span>
      <button
        type="button"
        onClick={notifications.enable}
        className="ml-auto shrink-0 rounded-md border px-2.5 py-1 text-xs font-medium transition-colors hover:bg-accent"
      >
        Enable
      </button>
    </div>
  )
}

function NotificationToggle({ notifications }: { notifications: Notifications }) {
  if (!notifications.supported) return null
  if (notifications.permission === 'denied') {
    return (
      <span className="px-2 text-xs text-muted-foreground">
        notifications blocked
      </span>
    )
  }
  const on = notifications.enabled
  const Icon = on ? Bell : BellOff
  return (
    <button
      type="button"
      onClick={() => (on ? notifications.disable() : notifications.enable())}
      aria-pressed={on}
      className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
    >
      <Icon className="size-3.5" />
      {on ? 'Notifications on' : 'Notify me'}
    </button>
  )
}

function ago(ts: string): string {
  const then = Date.parse(ts)
  if (Number.isNaN(then)) return ''
  const secs = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (secs < 60) return `${secs}s ago`
  const mins = Math.round(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.round(hours / 24)}d ago`
}
