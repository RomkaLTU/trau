import type { QueueItem } from './queue'

// The copy that keeps Remove from queue apart from the ticket Delete beside it:
// one ejects the work, the other purges the issue. Every removal says what it
// wipes.
const EJECTS_RUN =
  'The row goes, its saved progress is wiped and the ticket goes back to Ready — a later pickup starts a brand-new run.'

// removableFromQueue reports whether the X may take the row out. A running row
// is never removed in one gesture: Stop parks it at its checkpoint first, and the
// parked row is what a removal then wipes.
export function removableFromQueue(item: QueueItem): boolean {
  return item.status !== 'running'
}

export function removeFromQueueTitle(item: QueueItem): string {
  return `Remove ${item.id} from the queue?`
}

export function removeFromQueueWarning(item: QueueItem): string {
  const parts = [EJECTS_RUN]
  const subs = item.sub_issues?.length ?? 0
  if (subs > 0) {
    parts.push(
      subs === 1
        ? 'Its 1 sub-issue leaves the queue with it.'
        : `Its ${subs} sub-issues leave the queue with it.`,
    )
  }
  return parts.join(' ')
}

export const REMOVE_FROM_QUEUE_LABEL = 'Remove from queue'
