import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export type EyebrowGlyph =
  'action' | 'active' | 'idle' | 'done' | 'warn' | 'partial'

const GLYPHS: Record<EyebrowGlyph, string> = {
  action: '▸',
  active: '●',
  idle: '○',
  done: '✓',
  warn: '⚠',
  partial: '◔',
}

export function Eyebrow({
  glyph = 'action',
  children,
  className,
}: {
  glyph?: EyebrowGlyph
  children: ReactNode
  className?: string
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-2 font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground',
        className,
      )}
    >
      <span aria-hidden="true">{GLYPHS[glyph]}</span>
      {children}
    </span>
  )
}
