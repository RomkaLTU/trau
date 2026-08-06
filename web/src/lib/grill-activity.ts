import { useSyncExternalStore } from 'react'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Whether a thread shows what the agent is doing mid-turn is view state belonging to
// the reader rather than to any one session, so it lives in localStorage. It is on
// until someone switches it off: a turn that works before it answers reads as stalled
// without it.
const ACTIVITY_KEY = 'trau.grill.activity'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadActivityShown(): boolean {
  return browserStore()?.getItem(ACTIVITY_KEY) !== '0'
}

const listeners = new Set<() => void>()
let snapshot: boolean | null = null

function currentShown(): boolean {
  if (snapshot === null) snapshot = loadActivityShown()
  return snapshot
}

// Passing null drops the snapshot, so the next read comes off storage — which is what
// another tab's write leaves behind.
function publish(shown: boolean | null): void {
  snapshot = shown
  listeners.forEach((notify) => notify())
}

function onStorage(event: StorageEvent): void {
  if (event.key === null || event.key === ACTIVITY_KEY) publish(null)
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

export function storeActivityShown(shown: boolean): void {
  browserStore()?.setItem(ACTIVITY_KEY, shown ? '1' : '0')
  publish(shown)
}

// useActivityShown hands every surface the one preference, so flipping the switch in
// either header reaches the thread beside it — and the other tab — without a remount.
export function useActivityShown(): [boolean, (shown: boolean) => void] {
  return [useSyncExternalStore(subscribe, currentShown), storeActivityShown]
}

// A row holds about this many characters of detail at text-xs before it runs out of
// column. The budget is fixed rather than measured: the row is one line either way,
// and hovering it hands back the whole detail the hub sent.
export const ACTIVITY_DETAIL_BUDGET = 80

// The head of a detail says what the call is, the tail says what it was about — a
// filename, a flag, a redirect — so an over-long one is cut in the middle rather than
// at the end, where the informative part lives.
export function truncateActivityDetail(
  detail: string,
  budget: number = ACTIVITY_DETAIL_BUDGET,
): string {
  const chars = [...detail]
  if (chars.length <= budget) return detail
  const tail = detailTail(chars, budget)
  const head = budget - tail - 1
  if (head < 1) return `…${chars.slice(chars.length - budget + 1).join('')}`
  return `${chars.slice(0, head).join('')}…${chars.slice(chars.length - tail).join('')}`
}

const HEAD_MIN = 20

// A path is identified by its last segment, so the tail stretches to hold that segment
// whole — with the slash in front of it, so the cut still reads as a path. A segment
// long enough to leave no head is not worth the row it would eat, and falls back to
// the even split every other detail gets.
function detailTail(chars: string[], budget: number): number {
  const half = Math.max(1, Math.floor((budget - 1) / 2))
  const slash = chars.lastIndexOf('/')
  if (slash <= 0 || slash === chars.length - 1) return half
  const segment = chars.length - slash
  if (segment <= half) return half
  return segment <= budget - 1 - HEAD_MIN ? segment : half
}
