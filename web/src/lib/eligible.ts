import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import { queueCoveredIds, type QueueItem, type QueueKind } from './queue'

export interface EligibleTicket {
  id: string
  title: string
  labels: string[]
  parent?: string
  has_children: boolean
}

export interface EligibleResponse {
  repo: string
  repo_root: string
  tickets: EligibleTicket[]
}

async function fetchEligible(repo: string): Promise<EligibleResponse> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/eligible`,
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `eligible request failed: ${res.status}`)
  }
  return res.json()
}

export const eligibleQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['eligible', repo],
    queryFn: () => fetchEligible(repo),
    enabled: repo !== '',
    staleTime: 30_000,
  })

export interface AddAllItem {
  id: string
  kind: QueueKind
}

export interface AddAllPlan {
  items: AddAllItem[]
  epics: number
  tickets: number
}

// planAddAll turns the eligible list into an ordered set of enqueue operations
// for "Add all eligible". A sub-issue contributes its epic (enqueued once, as
// kind "epic", so the hub captures the whole epic — not just the eligible
// subset); a parentless epic goes in as an epic; the rest enqueue as plain
// tickets. Ids already in the queue, or covered by a queued epic's sub-issues,
// are dropped so a re-add is a no-op.
export function planAddAll(
  eligible: EligibleTicket[],
  queued: QueueItem[],
): AddAllPlan {
  const covered = queueCoveredIds(queued)

  const items: AddAllItem[] = []
  const planned = new Set<string>()

  const push = (id: string, kind: QueueKind) => {
    if (covered.has(id) || planned.has(id)) return
    planned.add(id)
    items.push({ id, kind })
  }

  for (const t of eligible) {
    if (t.parent) {
      push(t.parent, 'epic')
    } else if (t.has_children) {
      push(t.id, 'epic')
    } else {
      push(t.id, 'ticket')
    }
  }

  const epics = items.reduce((n, it) => (it.kind === 'epic' ? n + 1 : n), 0)
  return { items, epics, tickets: items.length - epics }
}

function plural(n: number, noun: string): string {
  return `${n} ${n === 1 ? noun : `${noun}s`}`
}

// addAllLabel renders the button text for a plan, e.g. "Add all eligible (2
// epics + 3 tickets)". Only non-empty groups appear.
export function addAllLabel(plan: AddAllPlan): string {
  const parts: string[] = []
  if (plan.epics > 0) parts.push(plural(plan.epics, 'epic'))
  if (plan.tickets > 0) parts.push(plural(plan.tickets, 'ticket'))
  return `Add all eligible (${parts.join(' + ')})`
}
