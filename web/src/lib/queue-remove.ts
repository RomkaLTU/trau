import type { QueueItem } from './queue'

// The copy that keeps Remove from queue apart from the ticket Delete beside it:
// one ejects the work, the other purges the issue. Every removal says what it
// wipes, and a running row also says its run stops first.
const EJECTS_RUN =
  'The row goes, its saved progress is wiped and the ticket goes back to Ready — a later pickup starts a brand-new run.'

const STOPS_RUN =
  'The run stops first, and the work it checkpointed goes with it.'

export function removeFromQueueTitle(item: QueueItem): string {
  return item.status === 'running'
    ? `Stop ${item.id} and remove it from the queue?`
    : `Remove ${item.id} from the queue?`
}

export function removeFromQueueWarning(item: QueueItem): string {
  const parts = item.status === 'running' ? [STOPS_RUN] : []
  parts.push(EJECTS_RUN)
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

export function removeFromQueueLabel(item: QueueItem): string {
  return item.status === 'running' ? 'Stop and remove' : 'Remove from queue'
}
