import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  RestartTimeout,
  canApply,
  checkedAgo,
  isSuccessor,
  needsAttention,
  pollMs,
  switchChannel,
  switchInFlight,
  updateQueryOptions,
  versionLabel,
  waitForSuccessor,
  type HubMark,
  type UpdateStatus,
} from './update'

afterEach(() => {
  vi.unstubAllGlobals()
})

function status(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    running: 'v2.1.0',
    onDisk: 'v2.1.0',
    latest: '2.1.0',
    restartPending: false,
    updateAvailable: false,
    installMethod: 'brew',
    upgradeCommand: 'brew upgrade --cask trau',
    checkedAt: null,
    checksEnabled: true,
    releaseUrl: 'https://github.com/RomkaLTU/trau/releases/tag/v2.1.0',
    applyState: { state: 'idle', message: '' },
    selfReloadPending: '',
    channel: 'release',
    channelRepo: '',
    channelRepos: [],
    channelSwitch: { state: 'idle', repoRoot: '', message: '' },
    releaseBinary: '',
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

describe('pollMs', () => {
  it('follows an idle hub live, so a reload asked for elsewhere reaches an open page', () => {
    expect(pollMs(status())).toBe(5000)
    expect(pollMs(undefined)).toBe(5000)
  })

  it('keeps following while a self-reload waits, so the note goes once it lands', () => {
    expect(pollMs(status({ selfReloadPending: '/repos/acme' }))).toBe(5000)
  })

  it('tightens onto a running apply to follow the brew output', () => {
    expect(pollMs(status({ applyState: { state: 'running', message: '' } }))).toBe(
      2000,
    )
  })

  it('tightens onto a channel switch so the restart is picked up promptly', () => {
    expect(
      pollMs(
        status({
          channelSwitch: { state: 'building', repoRoot: '/repos/trau', message: '' },
        }),
      ),
    ).toBe(2000)
  })
})

describe('switchInFlight', () => {
  it('is true while the hub builds and while it waits to restart', () => {
    for (const state of ['building', 'restarting'] as const) {
      expect(
        switchInFlight(
          status({ channelSwitch: { state, repoRoot: '/repos/trau', message: '' } }),
        ),
      ).toBe(true)
    }
  })

  it('is false once the switch settles, so a failure frees the action again', () => {
    expect(switchInFlight(status())).toBe(false)
    expect(
      switchInFlight(
        status({
          channelSwitch: { state: 'failed', repoRoot: '/repos/trau', message: 'boom' },
        }),
      ),
    ).toBe(false)
    expect(switchInFlight(undefined)).toBe(false)
  })
})

describe('updateQueryOptions', () => {
  it('takes its refetch interval from the status it last saw', () => {
    const interval = updateQueryOptions.refetchInterval as (query: never) => number
    const applying = status({ applyState: { state: 'running', message: '' } })
    expect(interval({ state: { data: status() } } as never)).toBe(pollMs(status()))
    expect(interval({ state: { data: applying } } as never)).toBe(pollMs(applying))
  })

  it('fills in the channel fields a hub from before the build channel omits', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => ({ running: 'v2.10.0' }),
      } as Response),
    )
    const fetchUpdate = updateQueryOptions.queryFn as () => Promise<UpdateStatus>

    const fetched = await fetchUpdate()

    expect(fetched.channel).toBe('release')
    expect(fetched.channelRepos).toEqual([])
    expect(fetched.channelSwitch).toEqual({
      state: 'idle',
      repoRoot: '',
      message: '',
    })
    expect(switchInFlight(fetched)).toBe(false)
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

describe('switchChannel', () => {
  function fetchMock(status: number, body: unknown = {}) {
    const mock = vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      json: async () => body,
    } as Response)
    vi.stubGlobal('fetch', mock)
    return mock
  }

  function sentBody(mock: ReturnType<typeof fetchMock>): unknown {
    const [, init] = mock.mock.calls[0] as [string, RequestInit]
    return JSON.parse(String(init.body))
  }

  it('names the repo to rebuild when switching to dev', async () => {
    const mock = fetchMock(202)

    await switchChannel('dev', '/src/acme')

    expect((mock.mock.calls[0] as [string])[0]).toContain('/api/v1/hub/channel')
    expect(sentBody(mock)).toEqual({ channel: 'dev', repo_root: '/src/acme' })
  })

  it('names no repo when switching back to release', async () => {
    const mock = fetchMock(202)

    await switchChannel('release')

    expect(sentBody(mock)).toEqual({ channel: 'release', repo_root: '' })
  })

  it('surfaces the reason the hub refused', async () => {
    fetchMock(404, { error: 'no release install found' })

    await expect(switchChannel('release')).rejects.toThrow(
      'no release install found',
    )
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
