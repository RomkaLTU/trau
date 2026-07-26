import { cn } from '@/lib/utils'

export function DiffCounts({
  additions,
  deletions,
  className,
}: {
  additions: number
  deletions: number
  className?: string
}) {
  return (
    <span className={cn('shrink-0 tabular-nums', className)}>
      <span className="text-done">+{additions}</span>{' '}
      <span className="text-fail">−{deletions}</span>
    </span>
  )
}
