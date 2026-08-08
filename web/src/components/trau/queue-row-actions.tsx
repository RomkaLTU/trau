import { RefreshCw, Square, X } from 'lucide-react'

import type { QueueItem } from '@/lib/queue'
import { removableFromQueue } from '@/lib/queue-remove'
import {
  STOP_RUN_HINT,
  STOP_RUN_LABEL,
  stopRunTitle,
  stopRunWarning,
} from '@/lib/queue-stop'

import { ConfirmDialog } from './confirm-dialog'

// RemoveFromQueueButton is the queue's own X: it ejects the row's work
// altogether — the saved progress goes and the ticket returns to Ready. A running
// row never gets it: removal there is two deliberate steps, Stop then remove the
// parked row, which is the contract the MCP admin surface has always held.
export function RemoveFromQueueButton({
  item,
  disabled,
  onRemove,
  tabIndex,
}: {
  item: QueueItem
  disabled: boolean
  onRemove: (id: string) => void
  tabIndex?: number
}) {
  if (!removableFromQueue(item)) return null

  return (
    <button
      type="button"
      tabIndex={tabIndex}
      onClick={() => onRemove(item.id)}
      disabled={disabled}
      title="Remove from queue (the ticket goes back to Ready)"
      aria-label={`Remove ${item.id} from queue`}
      className="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-fail disabled:pointer-events-none disabled:opacity-30"
    >
      <X className="size-3.5" aria-hidden="true" />
    </button>
  )
}

// StopRunButton is the running row's own action: it ends the child the way the
// queue-level Stop does, so the ticket parks at its checkpoint and stays
// resumable. The kill grace runs to 90s, so the button reads as waiting for the
// whole wait — off the same stopping flag the queue-level button uses.
export function StopRunButton({
  id,
  stopping,
  onStop,
  tabIndex,
}: {
  id: string
  stopping: boolean
  onStop: () => void
  tabIndex?: number
}) {
  return (
    <ConfirmDialog
      windowTitle="confirm"
      trigger={
        <button
          type="button"
          tabIndex={tabIndex}
          disabled={stopping}
          title={stopping ? 'Stopping…' : STOP_RUN_HINT}
          aria-label={stopping ? `Stopping ${id}…` : STOP_RUN_HINT}
          className="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-30"
        >
          {stopping ? (
            <RefreshCw className="size-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <Square className="size-3.5" aria-hidden="true" />
          )}
        </button>
      }
      title={stopRunTitle(id)}
      description={stopRunWarning(id)}
      confirmLabel={STOP_RUN_LABEL}
      destructive
      onConfirm={onStop}
    />
  )
}

export function QueueRowAction({
  item,
  busy,
  stopping,
  onRemove,
  onStop,
  tabIndex,
}: {
  item: QueueItem
  busy: boolean
  stopping: boolean
  onRemove: (id: string) => void
  onStop: () => void
  tabIndex?: number
}) {
  if (item.status === 'running') {
    return (
      <StopRunButton
        id={item.id}
        stopping={stopping}
        onStop={onStop}
        tabIndex={tabIndex}
      />
    )
  }
  return (
    <RemoveFromQueueButton
      item={item}
      disabled={busy}
      onRemove={onRemove}
      tabIndex={tabIndex}
    />
  )
}
