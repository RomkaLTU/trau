import { useSyncExternalStore } from 'react'

import { authHeaders } from './auth'

export type HubStatus = 'online' | 'unreachable'

export interface HubConnectivity {
  status: HubStatus
  attempt: number
  retryAt: number
  probing: boolean
}

// One failed request is a blip — a call racing a hub restart, a laptop waking
// up — and must not blank the app. A run of them is the hub being gone.
const FAILURE_THRESHOLD = 3

const BACKOFF_MS = [1000, 2000, 4000, 8000, 15_000]

export function retryDelayMs(attempt: number): number {
  return BACKOFF_MS[Math.min(attempt, BACKOFF_MS.length - 1)]
}

export function secondsUntil(retryAt: number, now: number): number {
  return Math.max(0, Math.ceil((retryAt - now) / 1000))
}

const ONLINE: HubConnectivity = {
  status: 'online',
  attempt: 0,
  retryAt: 0,
  probing: false,
}

const listeners = new Set<() => void>()
let snapshot: HubConnectivity = ONLINE
let failures = 0
let timer: ReturnType<typeof setTimeout> | null = null

function publish(next: HubConnectivity): void {
  snapshot = next
  for (const notify of listeners) notify()
}

export function hubConnectivity(): HubConnectivity {
  return snapshot
}

export function subscribeHub(fn: () => void): () => void {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

export function useHubConnectivity(): HubConnectivity {
  return useSyncExternalStore(subscribeHub, hubConnectivity, hubConnectivity)
}

// Only a transport failure counts as a failure: a 401 or a 500 is the hub
// answering.
export function reportHubFailure(): void {
  if (snapshot.status === 'unreachable') return
  failures++
  if (failures >= FAILURE_THRESHOLD) goOffline()
}

export function reportHubReachable(): void {
  if (snapshot.status === 'online') {
    failures = 0
    return
  }
  resetHubWatch()
}

export function resetHubWatch(): void {
  clearTimer()
  failures = 0
  publish(ONLINE)
}

// A cold start with nothing listening — the installed app opening on the
// service worker's cached shell — is conclusive on its own, so the boot probe
// skips the failure run.
export function startHubWatch(): void {
  void hubHealth().then((res) => {
    if (res === null) goOffline()
  })
}

export function retryHubNow(): void {
  if (snapshot.status === 'online' || snapshot.probing) return
  clearTimer()
  void runProbe(snapshot.attempt)
}

function goOffline(): void {
  if (snapshot.status === 'unreachable') return
  failures = 0
  scheduleProbe(0)
}

function scheduleProbe(attempt: number): void {
  clearTimer()
  const delay = retryDelayMs(attempt)
  publish({
    status: 'unreachable',
    attempt,
    retryAt: Date.now() + delay,
    probing: false,
  })
  timer = setTimeout(() => void runProbe(attempt), delay)
}

async function runProbe(attempt: number): Promise<void> {
  timer = null
  publish({ ...snapshot, probing: true })
  const reachable = (await hubHealth()) !== null
  // A poll or an SSE reconnect can beat the probe home. Its late rejection is
  // stale and must not put the screen back over a hub that is already
  // answering — that would skip the failure run entirely.
  if (snapshot.status === 'online') return
  if (reachable) {
    reportHubReachable()
    return
  }
  scheduleProbe(attempt + 1)
}

// null is the hub being gone; any answer counts as up, since a tokenless page
// gets a 401 from an exposed hub that is very much alive.
export async function hubHealth(): Promise<Response | null> {
  try {
    return await fetch('/api/v1/health', {
      headers: authHeaders(),
      cache: 'no-store',
    })
  } catch {
    return null
  }
}

function clearTimer(): void {
  if (timer === null) return
  clearTimeout(timer)
  timer = null
}
