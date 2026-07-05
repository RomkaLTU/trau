import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export function TerminalCard({
  title,
  children,
  className,
  bodyClassName,
  scanlines = false,
}: {
  title: string
  children: ReactNode
  className?: string
  bodyClassName?: string
  scanlines?: boolean
}) {
  return (
    <section
      className={cn(
        'relative flex flex-col overflow-hidden rounded-lg border border-border bg-card',
        className,
      )}
    >
      <header className="flex items-center gap-3 border-b border-border px-4 py-2.5">
        <div className="flex items-center gap-1.5" aria-hidden="true">
          <span className="size-2.5 rounded-full bg-fail" />
          <span className="size-2.5 rounded-full bg-warn" />
          <span className="size-2.5 rounded-full bg-done" />
        </div>
        <span className="font-mono text-xs text-muted-foreground">{title}</span>
      </header>
      <div
        className={cn(
          'relative flex-1 p-4',
          scanlines && 'scanlines',
          bodyClassName,
        )}
      >
        {children}
      </div>
    </section>
  )
}
