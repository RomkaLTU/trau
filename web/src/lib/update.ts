import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import type { Health } from './health'

export type ApplyPhase = 'idle' | 'running' | 'failed'

export type Channel = 'dev' | 'release'

export type ChannelPhase = 'idle' | 'building' | 'restarting' | 'failed'

// ChannelSwitch is how a switch to the dev channel is going. message carries the
// tail of the build output, and only once the switch failed.
export interface ChannelSwitch {
  state: ChannelPhase
  repoRoot: string
  message: string
}

// ChannelRepo is a registered repo the hub would accept a switch onto: one that
// has set HUB_SELF_RELOAD.
export interface ChannelRepo {
  name: string
  root: string
}

// ApplyState is how a one-click update is going. message carries the tail of the
// brew output, and only once the upgrade failed.
export interface ApplyState {
  state: ApplyPhase
  message: string
}

// ChannelFields are what the hub answers about the build it runs. channel is
// derived from where its executable sits and channelRepo is the repo that owns
// it when that build is a dev one. releaseBinary is the install a switch back
// would restart onto — empty when PATH holds none, and only ever set while the
// hub is on dev. supervised says launchd owns the process, so a switch rewrites
// its LaunchAgent before it restarts.
interface ChannelFields {
  channel: Channel
  channelRepo: string
  channelRepos: ChannelRepo[]
  channelSwitch: ChannelSwitch
  releaseBinary: string
  supervised: boolean
}

// UpdateStatus is the hub's /update resource. running is the version serving
// this page and onDisk the binary a restart would re-exec: an upgrade that has
// landed under a still-running hub shows the two apart until it restarts.
// selfReloadPending names the repo whose merge asked the hub to restart onto its
// own build, empty when nothing is waiting. upgradeCommand is what the package
// manager that installed this trau updates it with, empty when no manager owns
// it. latestNotes is the GitHub release body for latest, so it describes that
// tag whether or not the hub is behind it.
export interface UpdateStatus extends ChannelFields {
  running: string
  onDisk: string
  latest: string
  latestNotes: string
  restartPending: boolean
  updateAvailable: boolean
  installMethod: string
  upgradeCommand: string
  checkedAt: string | null
  checksEnabled: boolean
  releaseUrl: string
  applyState: ApplyState
  selfReloadPending: string
}

export interface RestartAck {
  restarting: boolean
  version: string
}

const LIVE_POLL_MS = 5000
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

// withChannelFields fills in what a hub from before the build channel answers
// without. Switching back to the installed release is how this page comes to be
// talking to one — every published trau predates the channel — so the absence is
// settled here instead of at each of the reads.
function withChannelFields(
  status: Omit<UpdateStatus, keyof ChannelFields> & Partial<ChannelFields>,
): UpdateStatus {
  return {
    channel: 'release',
    channelRepo: '',
    channelRepos: [],
    channelSwitch: { state: 'idle', repoRoot: '', message: '' },
    releaseBinary: '',
    supervised: false,
    ...status,
  }
}

async function fetchUpdate(): Promise<UpdateStatus> {
  const res = await apiFetch('/api/v1/update')
  if (!res.ok) {
    throw new Error(`update request failed: ${res.status}`)
  }
  return withChannelFields(await res.json())
}

// switchInFlight reports whether a channel switch is still going, which is what
// keeps the action busy and the page waiting for the successor.
export function switchInFlight(status: UpdateStatus | undefined): boolean {
  const state = status?.channelSwitch.state
  return state === 'building' || state === 'restarting'
}

// pollMs is how closely /update is followed. The resource carries hub state that
// turns over between two glances at the page — a self-reload waiting on the runs
// to finish, a brew apply mid-flight — so it is polled live either way, and the
// once-a-day release check simply rides along on the same requests. Work being
// watched as it lands gets a tighter interval: a brew apply for its output, a
// channel switch for the moment it arms the restart.
export function pollMs(status: UpdateStatus | undefined): number {
  const live = status?.applyState.state === 'running' || switchInFlight(status)
  return live ? APPLY_POLL_MS : LIVE_POLL_MS
}

export const updateQueryOptions = queryOptions({
  queryKey: ['update'],
  queryFn: fetchUpdate,
  refetchInterval: (query) => pollMs(query.state.data),
})

export async function checkForUpdates(): Promise<UpdateStatus> {
  const res = await apiFetch('/api/v1/update/check', { method: 'POST' })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'update check failed'))
  }
  return withChannelFields(await res.json())
}

// applyUpdate answers as soon as brew is under way; the outcome arrives on the
// applyState of a later /update, and a successful upgrade ends in a restart.
export async function applyUpdate(): Promise<UpdateStatus> {
  const res = await apiFetch('/api/v1/update/apply', { method: 'POST' })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'update failed'))
  }
  return withChannelFields(await res.json())
}

// switchChannel answers as soon as the switch is under way; the outcome arrives
// on the channelSwitch of a later /update. A dev switch rebuilds repoRoot first
// and restarts onto what that produced — asking for it from a dev hub is the
// manual rebuild of the tree already running. A release switch names no repo:
// the hub restarts onto the install it finds on PATH.
export async function switchChannel(
  channel: Channel,
  repoRoot = '',
): Promise<void> {
  const res = await apiFetch('/api/v1/hub/channel', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ channel, repo_root: repoRoot }),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'channel switch failed'))
  }
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
