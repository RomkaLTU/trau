import { expect, it } from 'vitest'

import {
  pulledCounts,
  syncDisabled,
  syncStatusLine,
  type SyncStatus,
} from '@/components/trau/backlog-sync-control'
import type { SyncResponse } from '@/lib/instances'

const NOW = Date.parse('2026-07-20T12:00:00Z')

function status(over: Partial<SyncStatus> = {}): SyncStatus {
  return { pending: false, now: NOW, ...over }
}

function pulled(over: Partial<SyncResponse> = {}): SyncResponse {
  return {
    repo: 'loop',
    provider: 'linear',
    issues: 3,
    comments: 5,
    removed: 0,
    syncedAt: '2026-07-20T12:00:00Z',
    ...over,
  }
}

function ago(seconds: number): string {
  return new Date(NOW - seconds * 1000).toISOString()
}

it('reports a pull in flight from either the health state or the mutation', () => {
  expect(syncStatusLine(status({ state: 'syncing' })).text).toBe('syncing…')
  expect(syncStatusLine(status({ pending: true })).text).toBe('syncing…')
})

it('surfaces the mutation error over a recorded one, both as a failure', () => {
  const recorded = syncStatusLine(status({ lastError: 'linear: 500' }))
  expect(recorded).toEqual({ text: 'linear: 500', failed: true })
  expect(
    syncStatusLine(status({ lastError: 'linear: 500', error: 'sync failed: 401' }))
      .text,
  ).toBe('sync failed: 401')
})

it('keeps syncing ahead of a stale error so a retry does not read as failing', () => {
  expect(
    syncStatusLine(status({ pending: true, lastError: 'linear: 500' })).text,
  ).toBe('syncing…')
})

it('shows the pull counts, naming removals only when there were some', () => {
  expect(syncStatusLine(status({ pulled: pulled() })).text).toBe(
    'pulled 3 issues · 5 comments',
  )
  expect(syncStatusLine(status({ pulled: pulled({ removed: 2 }) })).text).toBe(
    'pulled 3 issues · 5 comments · removed 2',
  )
})

it('settles to a ticking relative time, calling the first few seconds just now', () => {
  expect(syncStatusLine(status({ lastSyncedAt: ago(4) })).text).toBe(
    'synced just now',
  )
  expect(syncStatusLine(status({ lastSyncedAt: ago(42) })).text).toBe(
    'synced 42s ago',
  )
  expect(syncStatusLine(status({ lastSyncedAt: ago(185) })).text).toBe(
    'synced 3m 5s ago',
  )
  expect(syncStatusLine(status({ lastSyncedAt: ago(7500) })).text).toBe(
    'synced 2h 5m ago',
  )
})

it('says never synced for a tracker-backed repo with no stamp yet', () => {
  expect(syncStatusLine(status()).text).toBe('never synced')
  expect(syncStatusLine(status({ lastSyncedAt: '' })).text).toBe('never synced')
})

it('disables the button only for the sync this view started', () => {
  expect(syncDisabled(status({ pending: true }))).toBe(true)
  expect(syncDisabled(status({ state: 'syncing' }))).toBe(false)
  expect(syncDisabled(status({ state: 'syncing', lastSyncedAt: ago(90) }))).toBe(
    false,
  )
})

it('holds the counts of a pull of its own, and none from a coalesced sync', () => {
  const response = pulled()
  expect(pulledCounts({ status: 'pulled', response })).toBe(response)
  expect(pulledCounts({ status: 'syncing' })).toBeUndefined()
})

it('leaves a coalesced sync reading as syncing rather than as counts or a failure', () => {
  const line = syncStatusLine(
    status({
      state: 'syncing',
      pulled: pulledCounts({ status: 'syncing' }),
      lastSyncedAt: ago(90),
    }),
  )
  expect(line).toEqual({ text: 'syncing…', failed: false })
})
