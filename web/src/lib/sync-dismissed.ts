import { useSyncExternalStore } from 'react'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Whether a degraded repo's notice has been waved away is view state, not
// something the hub should carry, so it lives in localStorage.
const DISMISSED_KEY = 'trau.sync-degraded.dismissed'

// Dismissals records, per repo, the sync error the user waved away. The health
// endpoint is polled every 30s, so remembering the error itself — not just the
// repo — is what keeps a dismissal from also hiding the next, different failure.
export type Dismissals = Record<string, string>

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadDismissals(): Dismissals {
  const raw = browserStore()?.getItem(DISMISSED_KEY)
  if (!raw) return {}
  try {
    const marks: unknown = JSON.parse(raw)
    return isDismissals(marks) ? marks : {}
  } catch {
    return {}
  }
}

function storeDismissals(marks: Dismissals): void {
  browserStore()?.setItem(DISMISSED_KEY, JSON.stringify(marks))
}

function isDismissals(value: unknown): value is Dismissals {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

const listeners = new Set<() => void>()
let snapshot: Dismissals | null = null

function currentDismissals(): Dismissals {
  if (snapshot === null) snapshot = loadDismissals()
  return snapshot
}

// Passing null drops the snapshot, so the next read comes off storage — which is
// what another tab's write leaves behind.
function publish(marks: Dismissals | null): void {
  snapshot = marks
  listeners.forEach((notify) => notify())
}

function onStorage(event: StorageEvent): void {
  if (event.key === null || event.key === DISMISSED_KEY) publish(null)
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

function recordDismissal(repo: string, error: string): void {
  const marks = markDismissed(loadDismissals(), repo, error)
  storeDismissals(marks)
  publish(marks)
}

// useDismissals hands a surface the dismissals every gate on the page shares and
// the recorder that adds to them, so waving one notice away settles the others
// reading the same repo without a remount.
export function useDismissals(): [
  Dismissals,
  (repo: string, error: string) => void,
] {
  return [useSyncExternalStore(subscribe, currentDismissals), recordDismissal]
}

export function markDismissed(
  marks: Dismissals,
  repo: string,
  error: string,
): Dismissals {
  return { ...marks, [repo]: error }
}

// isDismissed reports whether this repo's current sync error is the one already
// waved away. A repo that starts failing for a new reason raises its notice again.
export function isDismissed(
  marks: Dismissals,
  repo: string,
  error: string,
): boolean {
  return marks[repo] === error
}
