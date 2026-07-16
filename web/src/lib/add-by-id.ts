import { IssueFetchError, type Issue } from './issues'
import { queueTerminal, type QueueItem } from './queue'

// statusWarning flags a fetched ticket whose status means running it now is
// probably not what the user wants — the real thing the confirm step protects
// against. A ready-but-unlabeled ticket is a softer, non-blocking note.
export function statusWarning(
  issue: Issue,
): { tone: 'warn' | 'info'; text: string } | null {
  switch (issue.group) {
    case 'done':
      return { tone: 'warn', text: 'This ticket is already done — running it again may re-open finished work.' }
    case 'canceled':
      return { tone: 'warn', text: 'This ticket is canceled — it is unlikely to be ready to run.' }
    case 'started':
      return { tone: 'warn', text: 'This ticket is in progress — it may already be running elsewhere.' }
    default:
      if (!issue.ready) {
        return { tone: 'info', text: 'This ticket is not carrying the ready label. You can still run it.' }
      }
      return null
  }
}

export interface AddByIdState {
  confirmed: boolean
  // wrongProject: the ticket was fetched but belongs to another project — shown
  // for context, queueing blocked (the server ownership guard is the backstop).
  wrongProject: boolean
  // confirmless: a repo with no direct tracker Reader can't confirm a ticket,
  // but queueing by raw id is still allowed. A not-found id stays blocked:
  // that's the typo the confirm guards.
  confirmless: boolean
  canQueue: boolean
}

export function addByIdState(
  submittedId: string,
  fetched: Issue | undefined,
  error: unknown,
): AddByIdState {
  const confirmed = submittedId !== '' && !!fetched
  const wrongProject = confirmed && !fetched.in_project
  const confirmless =
    submittedId !== '' &&
    error instanceof IssueFetchError &&
    error.kind === 'no-tracker'
  return {
    confirmed,
    wrongProject,
    confirmless,
    canQueue: (confirmed && !wrongProject) || confirmless,
  }
}

// pendingBehind counts the queued items a front-inserted ticket would run ahead
// of: every unsettled item except the ticket itself (a pending item re-queued
// with front moves to the front rather than duplicating).
export function pendingBehind(items: QueueItem[], id: string): number {
  return items.filter((it) => it.id !== id && !queueTerminal(it.status)).length
}

export function runNextCopy(id: string, behind: number): string {
  if (behind <= 0) return `Runs ${id} next.`
  return `Runs ${id} next, then ${behind} more queued ${behind === 1 ? 'item' : 'items'}.`
}
