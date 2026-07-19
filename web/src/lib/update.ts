import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Health } from './health'

export type ApplyPhase = 'idle' | 'running' | 'failed'

// ApplyState is how a one-click update is going. message carries the tail of the
// brew output, and only once the upgrade failed.
export interface ApplyState {
  state: ApplyPhase
  message: string
}

// UpdateStatus is the hub's /update resource. running is the version serving
// this page and onDisk the binary a restart would re-exec: an upgrade that has
// landed under a still-running hub shows the two apart until it restarts.
export interface UpdateStatus {
  running: string
  onDisk: string
  latest: string
  restartPending: boolean
  updateAvailable: boolean
  installMethod: string
  checkedAt: string | null
  checksEnabled: boolean
  releaseUrl: string
  applyState: ApplyState
}

export interface RestartAck {
  restarting: boolean
  version: string
}

const IDLE_POLL_MS = 30 * 60 * 1000
const APPLY_POLL_MS = 2000

// A hub that has not answered by then either failed to respawn or is wedged,
// and hub.log says which.
const RESTART_TIMEOUT_MS = 20_000
const PROBE_INTERVAL_MS = 500

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as {
    error?: string
    instructions?: string
  } | null
  if (!detail?.error) return `${fallback}: ${res.status}`
  return detail.instructions
    ? `${detail.error} — ${detail.instructions}`
    : detail.error
}

async function fetchUpdate(): Promise<UpdateStatus> {
  const res = await apiFetch('/api/v1/update')
  if (!res.ok) {
    throw new Error(`update request failed: ${res.status}`)
  }
  return res.json()
}

// An idle hub only needs the daily check reflected, so it refetches on focus and
// every half hour; a running apply is followed closely enough to watch brew work.
export const updateQueryOptions = queryOptions({
  queryKey: ['update'],
  queryFn: fetchUpdate,
  refetchInterval: (query) =>
    query.state.data?.applyState.state === 'running'
      ? APPLY_POLL_MS
      : IDLE_POLL_MS,
})

export async function checkForUpdates(): Promise<UpdateStatus> {
  const res = await apiFetch('/api/v1/update/check', { method: 'POST' })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'update check failed'))
  }
  return res.json()
}

// applyUpdate answers as soon as brew is under way; the outcome arrives on the
// applyState of a later /update, and a successful upgrade ends in a restart.
export async function applyUpdate(): Promise<UpdateStatus> {
  const res = await apiFetch('/api/v1/update/apply', { method: 'POST' })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'update failed'))
  }
  return res.json()
}

export async function restartHub(): Promise<RestartAck> {
  const res = await apiFetch('/api/v1/hub/restart', { method: 'POST' })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'restart failed'))
  }
  return res.json()
}

export interface HubMark {
  version: string
  uptime: number
}

function hubMark(health: Health): HubMark {
  return { version: health.version, uptime: health.uptime_seconds }
}

// isSuccessor decides whether the hub answering now is a different process than
// the one marked before. A restart onto the same build changes no version, so a
// reset uptime has to count too.
export function isSuccessor(before: HubMark, after: HubMark): boolean {
  return after.version !== before.version || after.uptime < before.uptime
}

export async function currentHubMark(): Promise<HubMark> {
  const res = await apiFetch('/api/v1/health')
  if (!res.ok) {
    throw new Error(`health request failed: ${res.status}`)
  }
  return hubMark(await res.json())
}

export class RestartTimeout extends Error {
  constructor() {
    super('the hub did not come back — check ~/.trau/hub.log')
    this.name = 'RestartTimeout'
  }
}

// waitForSuccessor polls across the gap where the hub is not listening at all,
// so a probe that throws is the expected mid-restart case rather than a failure.
export async function waitForSuccessor(
  before: HubMark,
  {
    timeoutMs = RESTART_TIMEOUT_MS,
    intervalMs = PROBE_INTERVAL_MS,
    probe = currentHubMark,
  } = {},
): Promise<HubMark> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const after = await probe().catch(() => null)
    if (after && isSuccessor(before, after)) return after
    if (Date.now() >= deadline) throw new RestartTimeout()
    await new Promise((resolve) => setTimeout(resolve, intervalMs))
  }
}

type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

const RESTARTED_KEY = 'trau.hub-restarted'

function browserStore(): Store | null {
  try {
    return globalThis.sessionStorage ?? null
  } catch {
    return null
  }
}

// The successor serves its own frontend assets, so picking it up means a full
// page reload — and the toast confirming it has to survive that reload.
export function markRestarted(version: string): void {
  browserStore()?.setItem(RESTARTED_KEY, version)
}

export function takeRestartedVersion(): string | null {
  const store = browserStore()
  const version = store?.getItem(RESTARTED_KEY) ?? null
  store?.removeItem(RESTARTED_KEY)
  return version
}

// needsAttention is the sidebar badge condition: an upgrade already on disk and
// a newer release both resolve in the same place, so both raise the same dot.
export function needsAttention(status: UpdateStatus | undefined): boolean {
  return Boolean(status && (status.restartPending || status.updateAvailable))
}

// trau never replaces a binary it does not own: only a Homebrew install updates
// in place, and only when something newer exists to move to.
export function canApply(status: UpdateStatus): boolean {
  return (
    status.installMethod === 'brew' &&
    (status.updateAvailable || status.restartPending)
  )
}

// The running version comes from `git describe` and already carries a v; a
// release tag reaches the API stripped of one. Only a numeric version gets one
// added, so a "dev" build keeps reading as one.
export function versionLabel(version: string): string {
  if (version === '') return '—'
  return /^\d/.test(version) ? `v${version}` : version
}

export function checkedAgo(
  checkedAt: string | null,
  now: number = Date.now(),
): string {
  if (!checkedAt) return 'never'
  const ms = Date.parse(checkedAt)
  if (Number.isNaN(ms)) return 'never'
  const secs = Math.max(0, Math.round((now - ms) / 1000))
  if (secs < 60) return 'just now'
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}
