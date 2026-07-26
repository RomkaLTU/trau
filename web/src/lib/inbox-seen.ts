import { useSyncExternalStore } from 'react'

import type { GrillSession } from './grill'
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

function storeSeen(marks: SeenMarks): void {
  browserStore()?.setItem(SEEN_KEY, JSON.stringify(marks))
}

function isMarks(value: unknown): value is SeenMarks {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

const listeners = new Set<() => void>()
let snapshot: SeenMarks | null = null

function currentMarks(): SeenMarks {
  if (snapshot === null) snapshot = loadSeen()
  return snapshot
}

// Passing null drops the snapshot, so the next read comes off storage — which is what
// another tab's write leaves behind.
function publish(marks: SeenMarks | null): void {
  snapshot = marks
  listeners.forEach((notify) => notify())
}

function onStorage(event: StorageEvent): void {
  if (event.key === null || event.key === SEEN_KEY) publish(null)
}

function subscribe(notify: () => void): () => void {
  if (listeners.size === 0) globalThis.addEventListener('storage', onStorage)
  listeners.add(notify)
  return () => {
    listeners.delete(notify)
    if (listeners.size === 0) {
      globalThis.removeEventListener('storage', onStorage)
    }
  }
}

// recordSeen advances one session's mark against the marks storage holds now. The
// dock and the rail both write this key, in the same tab and in others, so writing
// back the whole map a surface loaded would drop every mark the other one made since.
export function recordSeen(session: GrillSession): void {
  const stored = loadSeen()
  const marks = markSeen(stored, session.id, session.updated_at)
  if (marks === stored) return
  storeSeen(marks)
  publish(marks)
}

// useSeenMarks hands a surface the shared marks and the recorder that advances them.
// Both surfaces read the one store, so a mark either of them makes — or another tab
// makes against the same key — reaches the other without a remount.
export function useSeenMarks(): [SeenMarks, (session: GrillSession) => void] {
  return [useSyncExternalStore(subscribe, currentMarks), recordSeen]
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

// hasUnseenSession reports whether a session has moved on since the user last read it.
// A session that has moved on is asking something new; one never opened has never been
// read at all.
export function hasUnseenSession(marks: SeenMarks, session: GrillSession): boolean {
  const mark = marks[session.id]
  return mark === undefined || isNewer(session.updated_at, mark)
}

// hasUnseenQuestion reports whether an item's question is one the user has yet to
// read — the rail's warn dot.
export function hasUnseenQuestion(marks: SeenMarks, item: InboxItem): boolean {
  if (item.attention !== 'answer' || !item.session) return false
  return hasUnseenSession(marks, item.session)
}

function isNewer(a: string, b: string): boolean {
  return Date.parse(a) > Date.parse(b)
}
