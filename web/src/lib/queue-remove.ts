import type { QueueItem } from './queue'

// The copy that keeps Remove from queue apart from the ticket Delete beside it:
// one drops a row, the other purges the issue. Every removal says so, and a
// running row also says what stopping its run costs.
const KEEPS_TICKET =
  'Only the queue entry goes — the ticket, its runs and its tracker row are untouched, and it can be queued again.'

const STOPS_RUN =
  'The run stops first: work in progress is saved at the last checkpoint, exactly as Stop leaves it.'

export function removeFromQueueTitle(item: QueueItem): string {
  return item.status === 'running'
    ? `Stop ${item.id} and remove it from the queue?`
    : `Remove ${item.id} from the queue?`
}

export function removeFromQueueWarning(item: QueueItem): string {
  const parts = item.status === 'running' ? [STOPS_RUN] : []
  parts.push(KEEPS_TICKET)
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
