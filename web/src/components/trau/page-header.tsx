import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'
import { Eyebrow, type EyebrowGlyph } from '@/components/trau/eyebrow'

export function PageHeader({
  eyebrow,
  eyebrowGlyph = 'active',
  eyebrowClassName,
  title,
  description,
  actions,
  className,
}: {
  eyebrow?: ReactNode
  eyebrowGlyph?: EyebrowGlyph
  eyebrowClassName?: string
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  className?: string
}) {
  return (
    <header
      className={cn(
        'flex flex-col gap-2 border-b border-border px-8 py-6',
        className,
      )}
    >
      {eyebrow ? (
        <Eyebrow glyph={eyebrowGlyph} className={eyebrowClassName}>
          {eyebrow}
        </Eyebrow>
      ) : null}
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="text-balance font-sans text-2xl font-semibold tracking-tight text-foreground">
            {title}
          </h1>
          {description ? (
            <p className="text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
              {description}
            </p>
          ) : null}
        </div>
        {actions ? (
          <div className="flex items-center gap-2">{actions}</div>
        ) : null}
      </div>
    </header>
  )
}
