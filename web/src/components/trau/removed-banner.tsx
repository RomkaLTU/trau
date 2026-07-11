import { Trash2 } from 'lucide-react'

export function RemovedBanner() {
  return (
    <div
      role="status"
      className="flex items-start gap-2.5 rounded-lg border border-warn/50 bg-warn/12 px-4 py-3"
    >
      <Trash2 className="mt-0.5 size-4 shrink-0 text-warn" aria-hidden="true" />
      <div className="flex flex-col gap-0.5">
        <p className="font-mono text-sm font-medium text-warn">Removed from the tracker</p>
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          This ticket was deleted, archived, or moved out of the project and no longer exists
          upstream. The run and its artifacts are kept for reference.
        </p>
      </div>
    </div>
  )
}
