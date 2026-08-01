import { GitMerge } from 'lucide-react'

import { syncHeadline, type SyncState } from '@/lib/steps'

const REASSURANCE =
  'The run is healthy. The worktree holds a staged merge while the agent resolves it — switching branch or resetting now would throw that resolution away.'

// SyncStateBanner is the run view's explicit state while a base sync resolves
// conflicts: the half-merged worktree is the work, not an anomaly.
export function SyncStateBanner({ sync }: { sync: SyncState }) {
  return (
    <div
      role="status"
      className="flex items-start gap-2.5 rounded-lg border border-info/50 bg-info/12 px-4 py-3"
    >
      <GitMerge className="mt-0.5 size-4 shrink-0 text-info" aria-hidden="true" />
      <div className="flex flex-col gap-0.5">
        <p className="font-mono text-sm font-medium text-info">{syncHeadline(sync)}</p>
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">{REASSURANCE}</p>
      </div>
    </div>
  )
}

// SyncStateLine is the same state at row scale, taking the stepper's place on the
// Loop card so the row names the sync instead of a generic phase.
export function SyncStateLine({ sync }: { sync: SyncState }) {
  return (
    <p role="status" className="flex items-center gap-2 font-mono text-sm text-info">
      <GitMerge className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0">{syncHeadline(sync)}</span>
    </p>
  )
}
