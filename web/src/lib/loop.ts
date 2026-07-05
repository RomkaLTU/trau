import type { FeedEvent } from '@/lib/events'

export type LoopHaltKind = 'paused' | 'budget' | 'fault' | 'quarantined'

export interface LoopHalt {
  kind: LoopHaltKind
  ticket: string
  reason: string
}

const TERMINAL_STATES = new Set([
  'paused',
  'faulted',
  'quarantined',
  'merged',
])

function field(ev: FeedEvent, key: string): string {
  const v = ev.fields?.[key]
  return typeof v === 'string' ? v : ''
}

// deriveLoopHalt reads the reason a repo's loop stopped from its event feed. It
// classifies the newest terminal state_change: a rate-limit or re-auth pause, a
// budget give-up, a fault, or another quarantine. A clean merge as the newest
// terminal event means the loop did not halt — it returns null. Events are
// expected newest-first, matching the feed's ordering.
export function deriveLoopHalt(events: FeedEvent[]): LoopHalt | null {
  for (const ev of events) {
    if (ev.kind !== 'state_change') continue
    const state = field(ev, 'state')
    if (!TERMINAL_STATES.has(state)) continue

    const ticket = field(ev, 'ticket')
    const reason = field(ev, 'reason')
    switch (state) {
      case 'paused':
        return { kind: 'paused', ticket, reason }
      case 'faulted':
        return { kind: 'fault', ticket, reason }
      case 'quarantined':
        return {
          kind: /budget/i.test(reason) ? 'budget' : 'quarantined',
          ticket,
          reason,
        }
      default:
        return null
    }
  }
  return null
}
