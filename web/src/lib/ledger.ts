import type { Instance } from './instances'
import type { FailureClass, Run } from './runs'

export type LedgerBucket = 'active' | 'needs-you' | 'merged' | 'stopped'
export type LedgerTab = 'all' | LedgerBucket

export interface LedgerRow {
  run: Run
  // instance is the Working instance holding this ticket, present only for a live
  // row. A grazing or parked loop is between tickets and does not hold one.
  instance?: Instance
}

export const MERGED_CAP = 15

export function joinInstances(
  runs: Run[],
  instances: Instance[],
  repo: string,
): LedgerRow[] {
  const working = new Map<string, Instance>()
  for (const inst of instances) {
    if (inst.repo === repo && inst.ticket && inst.session_state === 'working') {
      working.set(inst.ticket, inst)
    }
  }
  return runs.map((run) => ({ run, instance: working.get(run.ticket) }))
}

// bucketOf assigns each row to exactly one bucket by precedence: a live loop
// working the ticket wins, then a failure class needs the user, then a merged
// checkpoint, and everything else is stopped. The buckets stay disjoint.
export function bucketOf(row: LedgerRow): LedgerBucket {
  if (row.instance) return 'active'
  if (row.run.failure_class) return 'needs-you'
  if (row.run.phase === 'merged') return 'merged'
  return 'stopped'
}

const DISPLAY_ORDER: Record<LedgerBucket, number> = {
  'needs-you': 0,
  active: 1,
  stopped: 2,
  merged: 3,
}

function byUpdatedDesc(a: Run, b: Run): number {
  return (b.updated_at ?? '').localeCompare(a.updated_at ?? '')
}

// sortRows orders the ledger needs-you → active → stopped → merged, newest first
// within each bucket, so blocked runs surface at the top.
export function sortRows(rows: LedgerRow[]): LedgerRow[] {
  return [...rows].sort((a, b) => {
    const delta = DISPLAY_ORDER[bucketOf(a)] - DISPLAY_ORDER[bucketOf(b)]
    return delta !== 0 ? delta : byUpdatedDesc(a.run, b.run)
  })
}

export function bucketCounts(rows: LedgerRow[]): Record<LedgerBucket, number> {
  const counts: Record<LedgerBucket, number> = {
    active: 0,
    'needs-you': 0,
    merged: 0,
    stopped: 0,
  }
  for (const row of rows) counts[bucketOf(row)] += 1
  return counts
}

export function rowsForTab(rows: LedgerRow[], tab: LedgerTab): LedgerRow[] {
  const sorted = sortRows(rows)
  return tab === 'all' ? sorted : sorted.filter((row) => bucketOf(row) === tab)
}

export interface CappedRows {
  rows: LedgerRow[]
  hidden: number
}

// capMerged trims the merged tail of the combined `all` view to MERGED_CAP rows,
// reporting how many it hid for a "+N more" expander; the merged-only tab passes
// expanded to show them all. Merged rows sort last, so the tail never hides
// another bucket.
export function capMerged(rows: LedgerRow[], expanded: boolean): CappedRows {
  if (expanded) return { rows, hidden: 0 }
  let mergedSeen = 0
  const kept: LedgerRow[] = []
  for (const row of rows) {
    if (bucketOf(row) === 'merged') {
      mergedSeen += 1
      if (mergedSeen > MERGED_CAP) continue
    }
    kept.push(row)
  }
  return { rows: kept, hidden: Math.max(0, mergedSeen - MERGED_CAP) }
}

export function formatAge(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${String(m % 60).padStart(2, '0')}m`
  return `${Math.floor(h / 24)}d`
}

const CHECKPOINT_LABELS: Record<string, string> = {
  '': 'queued',
  building: 'building',
  built: 'built',
  handed_off: 'handed off',
  verified: 'verified',
  pr_open: 'pr open',
  merged: 'merged',
}

// checkpointLabel reuses a stopped run's checkpoint as the stepper's mono label.
export function checkpointLabel(phase: string): string {
  return CHECKPOINT_LABELS[phase] ?? phase.replace(/_/g, ' ')
}

const FAILURE_LABELS: Record<FailureClass, string> = {
  paused: 'paused',
  faulted: 'faulted',
  gave_up: 'quarantined',
}

// attentionReason is a needs-you row's mono reason line: the loop's own
// failure_reason, falling back to the failure class when it wrote none.
export function attentionReason(run: Run): string {
  const reason = run.failure_reason?.trim()
  if (reason) return reason
  return run.failure_class ? FAILURE_LABELS[run.failure_class] : ''
}
