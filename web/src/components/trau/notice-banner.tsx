import { X } from 'lucide-react'

import { cn } from '@/lib/utils'

export type RunNotice = {
  tone: 'success' | 'warn' | 'error'
  text: string
}

const NOTICE_TONE: Record<
  RunNotice['tone'],
  { box: string; text: string; glyph: string; dismiss: string }
> = {
  success: {
    box: 'border-done/50 bg-done/12',
    text: 'text-done',
    glyph: '✓',
    dismiss: 'text-done/80 hover:bg-done/12 hover:text-done',
  },
  warn: {
    box: 'border-warn/50 bg-warn/12',
    text: 'text-warn',
    glyph: '⚠',
    dismiss: 'text-warn/80 hover:bg-warn/12 hover:text-warn',
  },
  error: {
    box: 'border-fail/50 bg-fail/12',
    text: 'text-fail',
    glyph: '✗',
    dismiss: 'text-fail/80 hover:bg-fail/12 hover:text-fail',
  },
}

export function NoticeBanner({
  notice,
  onDismiss,
}: {
  notice: RunNotice
  onDismiss: () => void
}) {
  const tone = NOTICE_TONE[notice.tone]
  return (
    <div
      role="status"
      className={cn('flex items-start justify-between gap-3 rounded-lg border px-4 py-3', tone.box)}
    >
      <div className="flex items-start gap-2.5">
        <span aria-hidden="true" className={cn('mt-0.5 font-mono text-sm', tone.text)}>
          {tone.glyph}
        </span>
        <p className={cn('font-mono text-sm leading-relaxed', tone.text)}>{notice.text}</p>
      </div>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss"
        className={cn('flex size-6 shrink-0 items-center justify-center rounded-md', tone.dismiss)}
      >
        <X className="size-4" aria-hidden="true" />
      </button>
    </div>
  )
}
