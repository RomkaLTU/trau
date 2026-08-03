import { afterEach, describe, expect, it, vi } from 'vitest'

import type { UpdateStatus } from './update'
import { loadToasted, shouldRecap, shouldToast } from './update-seen'

function stubStorage(seed: Record<string, string> = {}): void {
  const store = new Map(Object.entries(seed))
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => store.get(key) ?? null,
    setItem: (key: string, value: string) => void store.set(key, value),
  })
}

function status(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    running: '2.1.0',
    onDisk: '2.1.0',
    latest: '2.2.0',
    latestNotes: '',
    restartPending: false,
    updateAvailable: true,
    installMethod: 'brew',
    upgradeCommand: 'brew upgrade --cask trau',
    checkedAt: null,
    checksEnabled: true,
    releaseUrl: 'https://github.com/RomkaLTU/trau/releases/tag/v2.2.0',
    applyState: { state: 'idle', message: '' },
    selfReloadPending: '',
    channel: 'release',
    channelRepo: '',
    channelRepos: [],
    channelSwitch: { state: 'idle', repoRoot: '', message: '' },
    releaseBinary: '',
    supervised: false,
    ...over,
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('shouldToast', () => {
  it('announces a release nothing has been said about', () => {
    expect(shouldToast(status(), '')).toBe(true)
  })

  it('stays quiet about the release it already announced', () => {
    expect(shouldToast(status(), '2.2.0')).toBe(false)
  })

  it('speaks up again once a newer release lands', () => {
    expect(shouldToast(status({ latest: '2.3.0' }), '2.2.0')).toBe(true)
  })

  it('stays quiet while its poll trails the release another tab announced', () => {
    expect(shouldToast(status({ latest: '2.2.0' }), '2.3.0')).toBe(false)
  })

  it('stays quiet on a hub with nothing newer', () => {
    expect(shouldToast(status({ updateAvailable: false }), '')).toBe(false)
  })

  it('stays quiet when no release has been checked for', () => {
    expect(shouldToast(status({ latest: '' }), '')).toBe(false)
  })
})

function installed(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return status({
    running: '2.2.0',
    onDisk: '2.2.0',
    latest: '2.2.0',
    latestNotes: '## Highlights\n\n- the queue drains politely now',
    updateAvailable: false,
    ...over,
  })
}

describe('shouldRecap', () => {
  it('walks the user through the release just installed under them', () => {
    expect(shouldRecap(installed(), '2.1.0')).toBe(true)
  })

  it('leaves a browser that has never recorded a version alone', () => {
    expect(shouldRecap(installed(), '')).toBe(false)
  })

  it('stays quiet about the version it already recapped', () => {
    expect(shouldRecap(installed(), '2.2.0')).toBe(false)
  })

  it('stays quiet once a newer release has shipped past this hub', () => {
    expect(shouldRecap(installed({ latest: '2.3.0' }), '2.1.0')).toBe(false)
  })

  it('stays quiet when the release came with no notes', () => {
    expect(shouldRecap(installed({ latestNotes: '' }), '2.1.0')).toBe(false)
  })

  it('stays quiet on a build that names no release', () => {
    expect(
      shouldRecap(installed({ running: 'dev', latest: 'dev' }), '2.1.0'),
    ).toBe(false)
  })

  it('stays quiet on a hub running its own working tree', () => {
    expect(shouldRecap(installed({ channel: 'dev' }), '2.1.0')).toBe(false)
  })

  it('stays quiet where release checks are off', () => {
    expect(shouldRecap(installed({ checksEnabled: false }), '2.1.0')).toBe(
      false,
    )
  })
})

describe('shouldRecap and shouldToast', () => {
  it('never both speak on the same status', () => {
    const upToDate = installed()
    expect(shouldRecap(upToDate, '2.1.0')).toBe(true)
    expect(shouldToast(upToDate, '2.1.0')).toBe(false)

    const behind = status({ latestNotes: '## Highlights' })
    expect(shouldToast(behind, '2.1.0')).toBe(true)
    expect(shouldRecap(behind, '2.0.0')).toBe(false)
  })

  it('leaves a hub running the latest over an older binary to the toast', () => {
    const waiting = installed({
      onDisk: '2.1.0',
      restartPending: true,
      updateAvailable: true,
    })

    expect(shouldToast(waiting, '2.1.0')).toBe(true)
    expect(shouldRecap(waiting, '2.1.0')).toBe(false)
  })
})

describe('loadToasted', () => {
  it('reads back the version a session before it announced', () => {
    stubStorage({ 'trau.update.toasted': '2.2.0' })

    expect(shouldToast(status(), loadToasted())).toBe(false)
  })

  it('starts empty without storage rather than swallowing the toast', () => {
    expect(loadToasted()).toBe('')
    expect(shouldToast(status(), loadToasted())).toBe(true)
  })
})
