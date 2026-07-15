import type { InboxItem } from './inbox'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Which questions have been read is view state, not semantic state, so it lives in
// localStorage rather than on the session.
const SEEN_KEY = 'trau.inbox.seen'

// SeenMarks records, per session id, how far the user has read it. The rail lists
// sessions without their messages, so the mark is the session's updated_at — the
// hub bumps it on every appended message, making it the one watermark the rail and
// the open conversation can both quote.
export type SeenMarks = Record<string, string>

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadSeen(): SeenMarks {
  const raw = browserStore()?.getItem(SEEN_KEY)
  if (!raw) return {}
  try {
    const marks: unknown = JSON.parse(raw)
    return isMarks(marks) ? marks : {}
  } catch {
    return {}
  }
}

export function storeSeen(marks: SeenMarks): void {
  browserStore()?.setItem(SEEN_KEY, JSON.stringify(marks))
}

function isMarks(value: unknown): value is SeenMarks {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

// markSeen advances a session's mark, returning marks untouched when it already
// reads at least this far. The open session is followed over SSE while the rail's
// list trails it by a staleTime, so the two report different updated_at for the
// same session and only the newer of them means "read this far".
export function markSeen(
  marks: SeenMarks,
  sessionID: string,
  updatedAt: string,
): SeenMarks {
  const mark = marks[sessionID]
  if (mark !== undefined && !isNewer(updatedAt, mark)) return marks
  return { ...marks, [sessionID]: updatedAt }
}

// hasUnseenQuestion reports whether an item's question is one the user has yet to
// read — the rail's warn dot. A session that has moved on since it was last read is
// asking something new; one never opened has never been read at all.
export function hasUnseenQuestion(marks: SeenMarks, item: InboxItem): boolean {
  if (item.attention !== 'answer' || !item.session) return false
  const mark = marks[item.session.id]
  return mark === undefined || isNewer(item.session.updated_at, mark)
}

function isNewer(a: string, b: string): boolean {
  return Date.parse(a) > Date.parse(b)
}
