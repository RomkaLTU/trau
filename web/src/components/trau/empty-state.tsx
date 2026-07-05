import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export function EmptyState({
  message,
  actions,
  className,
}: {
  message: ReactNode
  actions?: ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'furrow-grid relative flex flex-col items-center justify-center gap-4 rounded-lg border border-border bg-card px-6 py-12 text-center',
        className,
      )}
    >
      <div
        className="hero-glow pointer-events-none absolute inset-0"
        aria-hidden="true"
      />
      <p className="relative font-sans text-sm text-muted-foreground">
        {message}
      </p>
      {actions ? (
        <div className="relative flex flex-wrap items-center justify-center gap-2">
          {actions}
        </div>
      ) : null}
    </div>
  )
}
