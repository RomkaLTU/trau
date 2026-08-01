import { useSyncExternalStore } from 'react'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Whether a thread shows what the agent is doing mid-turn is view state belonging to
// the reader rather than to any one session, so it lives in localStorage and is off
// until someone asks for it.
const ACTIVITY_KEY = 'trau.grill.activity'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadActivityShown(): boolean {
  return browserStore()?.getItem(ACTIVITY_KEY) === '1'
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
