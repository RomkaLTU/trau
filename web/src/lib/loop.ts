import type { FeedEvent } from '@/lib/events'
import type { Instance } from '@/lib/instances'
import { isActiveState, toSessionState } from '@/lib/overview'

export type LoopView = 'running' | 'builder'

// loopView picks which shape the Loop page renders. Draining always shows the
// running view, but a run can be in flight with the flag down — the drain
// disarms as its last item settles, and run-once or CLI starts never arm it —
// so an instance in an active session state that carries a ticket keeps the
// running view up until the run settles. Everything else gets the builder.
export function loopView(draining: boolean, instance?: Instance): LoopView {
  if (draining) return 'running'
  if (instance?.ticket && isActiveState(toSessionState(instance.session_state))) {
    return 'running'
  }
  return 'builder'
}

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
