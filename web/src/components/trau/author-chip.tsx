import { authorLabel, isMine } from '@/lib/ledger'
import type { Run } from '@/lib/runs'
import { cn } from '@/lib/utils'

export function AuthorChip({ run }: { run: Run }) {
  return (
    <span
      className={cn(
        'rounded-full border px-1.5 py-0.5 font-mono text-[0.65rem]',
        isMine(run)
          ? 'border-border text-muted-foreground'
          : 'border-info/40 bg-info/10 text-info',
      )}
    >
      {authorLabel(run)}
    </span>
  )
}
