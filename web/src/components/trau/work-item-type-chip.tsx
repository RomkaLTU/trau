import { cn } from '@/lib/utils'

// The chip shows the raw type name — "Feature", "User Story" — because that is
// what the board says; the normalized level only picks the colour. Emerald, sky,
// amber, violet and red are spoken for by the ready, queued and state chips beside
// it, so the two container levels take fuchsia and indigo and the leaf levels stay
// muted.
const LEVEL_STYLES: Record<string, string> = {
  epic: 'border-fuchsia-500/40 bg-fuchsia-500/5 text-fuchsia-600 dark:text-fuchsia-400',
  feature: 'border-indigo-500/40 bg-indigo-500/5 text-indigo-600 dark:text-indigo-400',
}

export function WorkItemTypeChip({ type, level }: { type: string; level?: string }) {
  return (
    <span
      title={level ? `${type} · ${level} level` : type}
      className={cn(
        'shrink-0 rounded-full border px-2 py-0.5 text-xs',
        LEVEL_STYLES[level ?? ''] ?? 'border-border text-muted-foreground',
      )}
    >
      {type}
    </span>
  )
}
