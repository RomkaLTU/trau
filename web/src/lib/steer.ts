import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { FeedEvent } from './events'
import type { FailureClass } from './runs'

export type SteerStatus = 'pending' | 'delivered' | 'expired'

export interface SteerNote {
  id: number
  ticket: string
  body: string
  status: SteerStatus
  delivered_phase?: string
  created_at?: string
  delivered_at?: string
}

export interface SteerNotesResponse {
  notes: SteerNote[]
}

export const STEER_PLACEHOLDER =
  'Steer the agent — delivered mid-run without stopping'

export const STEER_SETTLED_HINT =
  'This run has settled — a note typed now would expire before any agent read it.'

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

function steerURL(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/steer`
}

async function fetchSteerNotes(
  repo: string,
  ticket: string,
): Promise<SteerNotesResponse> {
  const res = await apiFetch(
    `${steerURL(repo)}?ticket=${encodeURIComponent(ticket)}`,
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'steer notes request failed'))
  }
  return res.json()
}

export async function queueSteerNote(
  repo: string,
  ticket: string,
  body: string,
): Promise<SteerNote> {
  const res = await apiFetch(steerURL(repo), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ticket, body }),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'steer note failed'))
  }
  return res.json()
}

export const steerNotesQueryKey = (repo: string, ticket: string) => [
  'steer',
  repo,
  ticket,
]

export const steerNotesQueryOptions = (repo: string, ticket: string) =>
  queryOptions({
    queryKey: steerNotesQueryKey(repo, ticket),
    queryFn: () => fetchSteerNotes(repo, ticket),
    refetchInterval: 3000,
    enabled: repo !== '' && ticket !== '',
  })

// A paused run is not settled: it is resumable, and its notes deliver at the next spawn.
export function steerSettled(run?: {
  phase: string
  failure_class?: FailureClass
}): boolean {
  if (!run) {
    return false
  }
  return (
    run.phase === 'merged' ||
    run.failure_class === 'faulted' ||
    run.failure_class === 'gave_up'
  )
}

// Only the delivery event carries mid-phase vs at-spawn; a note whose event aged
// out of the capped feed is absent, and reads as a plain delivery.
export function steerDeliveryModes(events: FeedEvent[]): Map<number, boolean> {
  const modes = new Map<number, boolean>()
  for (const ev of events) {
    if (ev.kind !== 'steer.delivered') continue
    const id = ev.fields?.note_id
    if (typeof id === 'number') {
      modes.set(id, ev.fields?.mid_phase === true)
    }
  }
  return modes
}

export function steerStatusLabel(note: SteerNote, midPhase?: boolean): string {
  switch (note.status) {
    case 'delivered': {
      const where = note.delivered_phase ? ` in ${note.delivered_phase}` : ''
      if (midPhase === undefined) {
        return `delivered${where}`
      }
      return `delivered${where} (${midPhase ? 'mid-phase' : 'at spawn'})`
    }
    case 'expired':
      return 'expired (run ended before delivery)'
    default:
      return 'queued — waiting for the next agent'
  }
}
