import { grillProgress, type GrillMessage } from './grill'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Whether the context panel is open is view state, not semantic state, so it lives
// in localStorage rather than the URL — the queue owns ?issue=.
const CONTEXT_KEY = 'trau.inbox.context'

// The panel only earns a column at xl; below it the same toggle raises an overlay
// over the chat, which is not something to do to a first visit uninvited.
const WIDE_QUERY = '(min-width: 80rem)'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

function wideViewport(): boolean {
  try {
    return globalThis.matchMedia?.(WIDE_QUERY).matches ?? true
  } catch {
    return true
  }
}

export function loadContextOpen(): boolean {
  const raw = browserStore()?.getItem(CONTEXT_KEY) ?? null
  return raw === null ? wideViewport() : raw === '1'
}

export function storeContextOpen(open: boolean): void {
  browserStore()?.setItem(CONTEXT_KEY, open ? '1' : '0')
}

export interface ContextRow {
  label: string
  value: string
}

// contextRows is the panel's meta list: when the issue was raised, whether it came
// from the tracker or was authored here, and how far its grilling has got.
export function contextRows(opts: {
  created?: string
  source?: string
  messages: GrillMessage[]
  now: Date
}): ContextRow[] {
  const { answered, total } = grillProgress(opts.messages)
  return [
    { label: 'created', value: createdAge(opts.created, opts.now) },
    { label: 'source', value: opts.source === 'internal' ? 'internal' : 'tracker' },
    {
      label: 'progress',
      value: `${answered} of ${total} question${total === 1 ? '' : 's'} answered`,
    },
  ]
}

// createdAge reads as an age rather than a date: triage cares that an issue has sat
// unclear for a week, not which Tuesday it was raised. A tracker ticket answered
// from a summary read carries no timestamp, hence the dash.
function createdAge(ts: string | undefined, now: Date): string {
  const then = ts ? Date.parse(ts) : Number.NaN
  if (Number.isNaN(then)) return '—'
  const secs = Math.max(0, Math.round((now.getTime() - then) / 1000))
  if (secs < 60) return 'just now'
  const mins = Math.round(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.round(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.round(hours / 24)}d ago`
}
