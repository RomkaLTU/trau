import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'

import { cn } from '@/lib/utils'

export function RunPageHeader({
  ticket,
  title,
  badges,
  actions,
  meta,
  className,
}: {
  ticket: string
  title?: string
  badges?: ReactNode
  actions?: ReactNode
  meta?: ReactNode
  className?: string
}) {
  return (
    <header className={cn('flex flex-col gap-2', className)}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex min-w-0 grow basis-80 items-center gap-3">
          <Link
            to="/runs"
            className="inline-flex shrink-0 items-center gap-1.5 font-mono text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            <ArrowLeft className="size-4" aria-hidden="true" />
            Runs
          </Link>
          <span className="shrink-0 font-mono text-sm text-primary">
            {ticket}
          </span>
          {title && (
            <h1
              title={title}
              className="truncate font-sans text-xl font-semibold tracking-tight text-foreground"
            >
              {title}
            </h1>
          )}
        </div>
        {badges && (
          <div className="flex flex-wrap items-center gap-3">{badges}</div>
        )}
        {actions && (
          <div className="flex flex-wrap items-center gap-2">{actions}</div>
        )}
      </div>
      {meta}
    </header>
  )
}
