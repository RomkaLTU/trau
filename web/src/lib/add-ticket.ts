import type { QueueItem, QueueKind, QueueResponse } from './queue'
import type { SearchResult } from './search'

// SETTLED_GROUPS are the status groups the loop would settle on sight. The hub's
// search ranks over every issue it stores, settled ones included, so the picker
// drops them here rather than offering work that is already over.
const SETTLED_GROUPS = new Set(['done', 'canceled'])

// PickerEmpty tells the two exhausted searches apart: nothing usable matched at
// all, or everything that matched is already lined up.
export type PickerEmpty = 'no-match' | 'all-queued'

export interface PickerList {
  rows: SearchResult[]
  empty: PickerEmpty | null
}

// pickerList narrows a search response to the rows the queue builder can offer.
// A settled issue is never a candidate; a candidate the queue already covers —
// as an item of its own or inside a queued epic — is dropped as a duplicate.
export function pickerList(
  results: SearchResult[],
  queued: QueueItem[],
): PickerList {
  const candidates = results.filter((r) => !SETTLED_GROUPS.has(r.group))
  if (candidates.length === 0) return { rows: [], empty: 'no-match' }

  const covered = new Set<string>()
  for (const it of queued) {
    covered.add(it.id)
    for (const sub of it.sub_issues ?? []) covered.add(sub.id)
  }

  const rows = candidates.filter((r) => !covered.has(r.id))
  return { rows, empty: rows.length === 0 ? 'all-queued' : null }
}

// toggleSelected checks or unchecks a row. Selection holds the results
// themselves, not their ids, so a pick survives the next search narrowing the
// list out from under it.
export function toggleSelected(
  selected: SearchResult[],
  row: SearchResult,
): SearchResult[] {
  return selected.some((s) => s.id === row.id)
    ? selected.filter((s) => s.id !== row.id)
    : [...selected, row]
}

export interface AddTicketItem {
  id: string
  kind: QueueKind
}

// planAddSelected turns the checked rows into enqueue operations in the order
// they were checked, which is the order they will run. An epic enqueues as kind
// "epic" so the hub captures its sub-issues; everything else is a plain ticket.
export function planAddSelected(selected: SearchResult[]): AddTicketItem[] {
  return selected.map((r) => ({
    id: r.id,
    kind: r.has_children ? 'epic' : 'ticket',
  }))
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

// addTickets enqueues the planned items one at a time, in order, so the queue
// lands the way the picker listed it. A failing id does not abort the batch: the
// rest still go in and the failures are reported together at the end. Each
// success publishes the queue it returned, so the card fills in as the batch
// lands rather than jumping at the finish.
export async function addTickets(
  items: AddTicketItem[],
  enqueueOne: (item: AddTicketItem) => Promise<QueueResponse>,
  onQueue: (res: QueueResponse) => void,
): Promise<void> {
  const errors: string[] = []
  for (const item of items) {
    try {
      onQueue(await enqueueOne(item))
    } catch (err) {
      errors.push(`${item.id}: ${errorText(err)}`)
    }
  }
  if (errors.length > 0) throw new Error(errors.join('\n'))
}
