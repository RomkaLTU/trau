import { afterEach, describe, expect, it, vi } from 'vitest'

import type { UpdateStatus } from './update'
import { loadToasted, shouldToast } from './update-seen'

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
