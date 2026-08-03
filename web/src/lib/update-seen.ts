import { useSyncExternalStore } from 'react'

import type { UpdateStatus } from './update'

type Store = Pick<Storage, 'getItem' | 'setItem'>

// Which release the user has already been told about is view state, not
// something the hub should carry, so it lives in localStorage — a reload stays
// quiet about a version already announced, and only a newer one speaks up.
const TOASTED_KEY = 'trau.update.toasted'

// A mark of its own: the toast is about the release ahead, the recap about the
// one now running, and an upgrade settles the two at different moments.
const RECAPPED_KEY = 'trau.whats-new.seen'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

// versionMark is one localStorage key followed across tabs. The mark only ever
// walks forward, so a tab whose poll still trails another's cannot talk it back
// down to a release already announced.
function versionMark(key: string) {
  const listeners = new Set<() => void>()
  let snapshot: string | null = null

  function load(): string {
    return browserStore()?.getItem(key) ?? ''
  }

  function current(): string {
    if (snapshot === null) snapshot = load()
    return snapshot
  }

  // Passing null drops the snapshot, so the next read comes off storage — which
  // is what another tab's write leaves behind.
  function publish(version: string | null): void {
    snapshot = version
    listeners.forEach((notify) => notify())
  }

  function onStorage(event: StorageEvent): void {
    if (event.key === null || event.key === key) publish(null)
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

  // record settles against what storage holds now: another tab may have moved
  // the mark since this one last read the key.
  function record(version: string): void {
    const stored = load()
    if (!isNewer(version, stored)) {
      publish(stored)
      return
    }
    browserStore()?.setItem(key, version)
    publish(version)
  }

  return { load, current, subscribe, record }
}

const toastedMark = versionMark(TOASTED_KEY)

export function loadToasted(): string {
  return toastedMark.load()
}

// useToastedVersion hands the notifier the release it has already announced and
// the recorder that settles the next one, so a second tab agrees about what has
// been said without waiting for a reload.
export function useToastedVersion(): [string, (version: string) => void] {
  return [
    useSyncExternalStore(toastedMark.subscribe, toastedMark.current),
    toastedMark.record,
  ]
}

// The recap mark names whichever hub a tab last came in on, so unlike the
// announcement it has to settle downwards too. That leaves no order to converge
// on, so it is read and written straight off storage instead of watched: a tab
// answering another one's write would only trade the key back and forth.
export function loadRecapped(): string {
  return browserStore()?.getItem(RECAPPED_KEY) ?? ''
}

export function recordRecapped(version: string): void {
  browserStore()?.setItem(RECAPPED_KEY, version)
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

// shouldRecap reports whether this load is the first one on a version that has
// just been installed. An unset mark is a browser that has never watched an
// upgrade land rather than one that missed a release, so it earns no recap; nor
// does a hub with an update still to take — one whose notes belong to the toast,
// whether it is behind the latest tag or already running it over a binary
// waiting on disk.
export function shouldRecap(status: UpdateStatus, recapped: string): boolean {
  return (
    recapped !== '' &&
    status.running !== recapped &&
    status.running !== 'dev' &&
    status.channel === 'release' &&
    status.checksEnabled &&
    !status.updateAvailable &&
    status.running === status.latest &&
    status.latestNotes !== ''
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
