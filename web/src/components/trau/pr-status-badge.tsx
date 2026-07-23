import { prStatusPill } from '@/lib/overview'
import type { PRStatus } from '@/lib/runs'
import { StatusPill } from './status-pill'

export function PRStatusBadge({
  status,
  className,
}: {
  status?: PRStatus
  className?: string
}) {
  const pill = prStatusPill(status)
  if (!pill) {
    return null
  }
  return <StatusPill state={pill.state} label={pill.label} className={className} />
}
