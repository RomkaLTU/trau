import { useEffect, useState } from 'react'
import { Check, Copy, TriangleAlert } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/clipboard'
import { cn } from '@/lib/utils'

const COPY_FEEDBACK = {
  idle: { icon: Copy, tone: '', status: '' },
  copied: { icon: Check, tone: 'text-done', status: 'copied' },
  failed: {
    icon: TriangleAlert,
    tone: 'text-destructive',
    status: 'copy failed',
  },
} as const

export function CopyButton({
  value,
  label,
  className,
}: {
  value: string
  label: string
  className?: string
}) {
  const [state, setState] = useState<keyof typeof COPY_FEEDBACK>('idle')
  const { icon: Icon, tone, status } = COPY_FEEDBACK[state]

  useEffect(() => {
    if (state === 'idle') return
    const timer = setTimeout(() => setState('idle'), 1600)
    return () => clearTimeout(timer)
  }, [state])

  return (
    <Button
      type="button"
      variant="outline"
      size="icon-sm"
      aria-label={label}
      className={cn('shrink-0', className)}
      onClick={() => {
        void copyText(value).then((ok) => setState(ok ? 'copied' : 'failed'))
      }}
    >
      <Icon className={cn('size-3.5', tone)} aria-hidden="true" />
      <span className="sr-only" role="status">
        {status}
      </span>
    </Button>
  )
}
