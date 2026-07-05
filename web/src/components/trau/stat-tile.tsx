import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export function StatTile({
  label,
  value,
  hint,
  progress,
  valueClassName,
  className,
}: {
  label: string
  value: ReactNode
  hint?: ReactNode
  progress?: { value: number; max: number }
  valueClassName?: string
  className?: string
}) {
  const pct =
    progress && progress.max > 0
      ? Math.min(100, Math.max(0, (progress.value / progress.max) * 100))
      : null

  return (
    <div
      className={cn(
        'flex flex-col gap-2 rounded-lg border border-border bg-card p-4',
        className,
      )}
    >
      <span className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </span>
      <span
        className={cn('font-mono text-2xl text-foreground', valueClassName)}
      >
        {value}
      </span>
      {pct !== null ? (
        <div
          className="mt-1 h-1 w-full overflow-hidden rounded-full bg-secondary"
          role="progressbar"
          aria-valuenow={progress!.value}
          aria-valuemin={0}
          aria-valuemax={progress!.max}
          aria-label={label}
        >
          <div
            className="h-full rounded-full bg-primary"
            style={{ width: `${pct}%` }}
          />
        </div>
      ) : null}
      {hint ? (
        <span className="font-mono text-[0.65rem] text-muted-foreground">
          {hint}
        </span>
      ) : null}
    </div>
  )
}
