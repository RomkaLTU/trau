import { type StateGroup } from '@/lib/backlog'
import { cn } from '@/lib/utils'

// Emerald and sky are spoken for by the neighbouring "ready" and "queued" chips,
// so done takes violet rather than a green.
const GROUP_STYLES: Record<StateGroup, string> = {
  backlog: 'text-muted-foreground/70',
  unstarted: 'text-muted-foreground',
  started: 'border-amber-500/40 bg-amber-500/5 text-amber-600 dark:text-amber-400',
  done: 'border-violet-500/40 bg-violet-500/5 text-violet-600 dark:text-violet-400',
  canceled: 'border-red-500/30 bg-red-500/5 text-red-600/80 dark:text-red-400/80',
  unknown: 'text-muted-foreground',
}

export function StateGroupChip({ group }: { group: string }) {
  return (
    <span
      className={cn(
        'rounded-full border px-2 py-0.5 text-xs',
        GROUP_STYLES[group as StateGroup] ?? GROUP_STYLES.unknown,
      )}
    >
      {group}
    </span>
  )
}
