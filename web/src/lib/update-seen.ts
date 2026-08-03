import { useSyncExternalStore } from 'react'

import type { UpdateStatus } from './update'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Which release the user has already been told about is view state, not
// something the hub should carry, so it lives in localStorage — a reload stays
// quiet about a version already announced, and only a newer one speaks up.
const TOASTED_KEY = 'trau.update.toasted'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadToasted(): string {
  return browserStore()?.getItem(TOASTED_KEY) ?? ''
}

const listeners = new Set<() => void>()
let snapshot: string | null = null

function currentToasted(): string {
  if (snapshot === null) snapshot = loadToasted()
  return snapshot
}

// Passing null drops the snapshot, so the next read comes off storage — which is
// what another tab's write leaves behind.
function publish(version: string | null): void {
  snapshot = version
  listeners.forEach((notify) => notify())
}

function onStorage(event: StorageEvent): void {
  if (event.key === null || event.key === TOASTED_KEY) publish(null)
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

// recordToasted advances the mark against what storage holds now. Another tab
// may have announced a newer release since this one last read the key, and an
// announcement never walks backwards.
function recordToasted(version: string): void {
  const stored = loadToasted()
  if (!isNewer(version, stored)) {
    publish(stored)
    return
  }
  browserStore()?.setItem(TOASTED_KEY, version)
  publish(version)
}

// useToastedVersion hands the notifier the release it has already announced and
// the recorder that settles the next one, so a second tab agrees about what has
// been said without waiting for a reload.
export function useToastedVersion(): [string, (version: string) => void] {
  return [useSyncExternalStore(subscribe, currentToasted), recordToasted]
}

// shouldToast reports whether this status names a release worth announcing. A
// hub with nothing newer — a dev build, checks off, a version already toasted —
// says nothing. Tabs poll on their own clocks, so one still holding the previous
// latest sees the mark another tab has just advanced: only a release newer than
// the mark is news, or that tab would announce its stale version straight back.
export function shouldToast(status: UpdateStatus, toasted: string): boolean {
  return (
    status.updateAvailable &&
    status.latest !== '' &&
    isNewer(status.latest, toasted)
  )
}

function isNewer(version: string, mark: string): boolean {
  const a = parts(version)
  const b = parts(mark)
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const diff = (a[i] ?? 0) - (b[i] ?? 0)
    if (diff !== 0) return diff > 0
  }
  return false
}

function parts(version: string): number[] {
  return version
    .replace(/^v/, '')
    .split('.')
    .map((part) => Number.parseInt(part, 10) || 0)
}
