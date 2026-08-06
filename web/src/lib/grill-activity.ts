import { useSyncExternalStore } from 'react'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Whether a thread shows what the agent is doing mid-turn is view state belonging to
// the reader rather than to any one session, so it lives in localStorage. It is on
// until someone switches it off: a turn that works before it answers reads as stalled
// without it.
const ACTIVITY_KEY = 'trau.grill.activity'

// Whether the feed stands open is the same kind of state one level down: the inbox
// remounts the thread on every focus switch, so component state would forget the
// choice between two clicks.
const ACTIVITY_OPEN_KEY = 'trau.grill.activity.open'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

interface Preference {
  load: () => boolean
  store: (on: boolean) => void
  subscribe: (notify: () => void) => () => void
  current: () => boolean
}

// Both preferences default on, so storage holds '0' for the switched-off case and
// anything else — including nothing at all — reads as on.
function preference(key: string): Preference {
  const listeners = new Set<() => void>()
  let snapshot: boolean | null = null

  const load = (): boolean => browserStore()?.getItem(key) !== '0'

  // Passing null drops the snapshot, so the next read comes off storage — which is
  // what another tab's write leaves behind.
  const publish = (on: boolean | null): void => {
    snapshot = on
    listeners.forEach((notify) => notify())
  }

  const onStorage = (event: StorageEvent): void => {
    if (event.key === null || event.key === key) publish(null)
  }

  return {
    load,
    store: (on) => {
      browserStore()?.setItem(key, on ? '1' : '0')
      publish(on)
    },
    subscribe: (notify) => {
      if (listeners.size === 0) globalThis.addEventListener('storage', onStorage)
      listeners.add(notify)
      return () => {
        listeners.delete(notify)
        if (listeners.size === 0) {
          globalThis.removeEventListener('storage', onStorage)
        }
      }
    },
    current: () => {
      if (snapshot === null) snapshot = load()
      return snapshot
    },
  }
}

const shownPreference = preference(ACTIVITY_KEY)
const openPreference = preference(ACTIVITY_OPEN_KEY)

export function loadActivityShown(): boolean {
  return shownPreference.load()
}

export function storeActivityShown(shown: boolean): void {
  shownPreference.store(shown)
}

// useActivityShown hands every surface the one preference, so flipping the switch in
// either header reaches the thread beside it — and the other tab — without a remount.
export function useActivityShown(): [boolean, (shown: boolean) => void] {
  return [
    useSyncExternalStore(shownPreference.subscribe, shownPreference.current),
    storeActivityShown,
  ]
}

export function loadActivityOpen(): boolean {
  return openPreference.load()
}

export function storeActivityOpen(open: boolean): void {
  openPreference.store(open)
}

export function useActivityOpen(): [boolean, (open: boolean) => void] {
  return [
    useSyncExternalStore(openPreference.subscribe, openPreference.current),
    storeActivityOpen,
  ]
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
