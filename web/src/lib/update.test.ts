import { describe, expect, it } from 'vitest'

import {
  RestartTimeout,
  canApply,
  checkedAgo,
  isSuccessor,
  needsAttention,
  versionLabel,
  waitForSuccessor,
  type HubMark,
  type UpdateStatus,
} from './update'

function status(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    running: 'v2.1.0',
    onDisk: 'v2.1.0',
    latest: '2.1.0',
    restartPending: false,
    updateAvailable: false,
    installMethod: 'brew',
    checkedAt: null,
    checksEnabled: true,
    releaseUrl: 'https://github.com/RomkaLTU/trau/releases/tag/v2.1.0',
    applyState: { state: 'idle', message: '' },
    ...over,
  }
}

describe('needsAttention', () => {
  it('stays quiet on a current hub', () => {
    expect(needsAttention(status())).toBe(false)
  })

  it('badges a newer binary already on disk', () => {
    expect(needsAttention(status({ restartPending: true }))).toBe(true)
  })

  it('badges a newer release', () => {
    expect(needsAttention(status({ updateAvailable: true }))).toBe(true)
  })

  it('stays quiet before the status arrives', () => {
    expect(needsAttention(undefined)).toBe(false)
  })
})

describe('canApply', () => {
  it('offers an in-place update on brew with something newer', () => {
    expect(canApply(status({ updateAvailable: true }))).toBe(true)
    expect(canApply(status({ restartPending: true }))).toBe(true)
  })

  it('never offers one on an install trau does not own', () => {
    expect(
      canApply(status({ installMethod: 'other', updateAvailable: true })),
    ).toBe(false)
  })

  it('never offers one when nothing is newer', () => {
    expect(canApply(status())).toBe(false)
  })
})

describe('isSuccessor', () => {
  const before: HubMark = { version: 'v2.1.0', uptime: 400 }

  it('recognizes a restart onto a new version', () => {
    expect(isSuccessor(before, { version: 'v2.2.0', uptime: 900 })).toBe(true)
  })

  it('recognizes a restart onto the same version by its reset uptime', () => {
    expect(isSuccessor(before, { version: 'v2.1.0', uptime: 2 })).toBe(true)
  })

  it('rejects the hub that answered before', () => {
    expect(isSuccessor(before, { version: 'v2.1.0', uptime: 410 })).toBe(false)
  })
})

describe('waitForSuccessor', () => {
  const before: HubMark = { version: 'v2.1.0', uptime: 400 }

  it('returns the successor once it answers', async () => {
    const answers: HubMark[] = [
      { version: 'v2.1.0', uptime: 402 },
      { version: 'v2.2.0', uptime: 1 },
    ]
    const after = await waitForSuccessor(before, {
      intervalMs: 1,
      probe: () => Promise.resolve(answers.shift() ?? before),
    })
    expect(after.version).toBe('v2.2.0')
  })

  it('treats an unreachable hub as mid-restart rather than a failure', async () => {
    let attempts = 0
    const after = await waitForSuccessor(before, {
      intervalMs: 1,
      probe: () => {
        attempts += 1
        if (attempts < 3) return Promise.reject(new Error('connection refused'))
        return Promise.resolve({ version: 'v2.1.0', uptime: 1 })
      },
    })
    expect(after.uptime).toBe(1)
    expect(attempts).toBe(3)
  })

  it('gives up once the successor is overdue', async () => {
    await expect(
      waitForSuccessor(before, {
        timeoutMs: 5,
        intervalMs: 1,
        probe: () => Promise.reject(new Error('connection refused')),
      }),
    ).rejects.toBeInstanceOf(RestartTimeout)
  })
})

describe('versionLabel', () => {
  it('prefixes a bare release tag', () => {
    expect(versionLabel('2.2.0')).toBe('v2.2.0')
  })

  it('leaves an already-prefixed version alone', () => {
    expect(versionLabel('v2.1.0')).toBe('v2.1.0')
  })

  it('leaves a non-numeric build alone', () => {
    expect(versionLabel('dev')).toBe('dev')
  })

  it('renders an unknown version as a dash', () => {
    expect(versionLabel('')).toBe('—')
  })
})

describe('checkedAgo', () => {
  const now = Date.parse('2026-07-19T12:00:00Z')

  it('reads as never before the first check', () => {
    expect(checkedAgo(null, now)).toBe('never')
  })

  it('reports the age of the last check', () => {
    expect(checkedAgo('2026-07-19T11:59:30Z', now)).toBe('just now')
    expect(checkedAgo('2026-07-19T11:30:00Z', now)).toBe('30m ago')
    expect(checkedAgo('2026-07-19T09:00:00Z', now)).toBe('3h ago')
    expect(checkedAgo('2026-07-17T12:00:00Z', now)).toBe('2d ago')
  })
})
