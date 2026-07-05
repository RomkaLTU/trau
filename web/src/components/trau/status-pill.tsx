import { cn } from '@/lib/utils'

export type RunState =
  'active' | 'success' | 'fail' | 'warn' | 'verify' | 'todo' | 'info'

const STATE_STYLES: Record<RunState, { glyph: string; className: string }> = {
  active: { glyph: '●', className: 'border-teal/60 bg-teal/12 text-teal' },
  success: { glyph: '✓', className: 'border-done/60 bg-done/12 text-done' },
  fail: { glyph: '✗', className: 'border-fail/60 bg-fail/12 text-fail' },
  warn: { glyph: '⚠', className: 'border-warn/60 bg-warn/12 text-warn' },
  verify: { glyph: '◔', className: 'border-info/60 bg-info/12 text-info' },
  todo: { glyph: '○', className: 'border-faint/60 bg-faint/12 text-faint' },
  info: { glyph: '●', className: 'border-info/60 bg-info/12 text-info' },
}

export function StatusPill({
  state,
  label,
  className,
}: {
  state: RunState
  label: string
  className?: string
}) {
  const { glyph, className: stateClass } = STATE_STYLES[state]
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 font-mono text-xs',
        stateClass,
        className,
      )}
    >
      <span aria-hidden="true">{glyph}</span>
      {label}
    </span>
  )
}
