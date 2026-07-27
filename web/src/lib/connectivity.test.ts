import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  hubConnectivity,
  reportHubFailure,
  reportHubReachable,
  resetHubWatch,
  retryDelayMs,
  retryHubNow,
  secondsUntil,
  startHubWatch,
} from './connectivity'
import { killHub, stubHub } from './connectivity-fixtures'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  resetHubWatch()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('retryDelayMs', () => {
  it('backs off and then holds a ceiling', () => {
    expect([0, 1, 2, 3, 4, 5, 12].map(retryDelayMs)).toEqual([
      1000, 2000, 4000, 8000, 15_000, 15_000, 15_000,
    ])
  })
})

describe('secondsUntil', () => {
  it('rounds a partial second up so the countdown never shows 0 early', () => {
    expect(secondsUntil(1200, 0)).toBe(2)
  })

  it('floors at zero once the probe is due', () => {
    expect(secondsUntil(1000, 4000)).toBe(0)
  })
})

describe('failure threshold', () => {
  it('rides out a blip', () => {
    reportHubFailure()
    reportHubFailure()
    expect(hubConnectivity().status).toBe('online')
  })

  it('trips on a run of failures', () => {
    killHub()
    expect(hubConnectivity()).toMatchObject({ status: 'unreachable', attempt: 0 })
  })

  it('starts the run over once something reaches the hub', () => {
    reportHubFailure()
    reportHubFailure()
    reportHubReachable()
    reportHubFailure()
    reportHubFailure()
    expect(hubConnectivity().status).toBe('online')
  })
})

describe('recovery', () => {
  it('leaves the state on its own once a probe answers', async () => {
    let listening = false
    stubHub(() => listening)
    killHub()

    await vi.advanceTimersByTimeAsync(1000)
    expect(hubConnectivity()).toMatchObject({
      status: 'unreachable',
      attempt: 1,
    })

    listening = true
    await vi.advanceTimersByTimeAsync(2000)
    expect(hubConnectivity().status).toBe('online')
  })

  it('backs off between probes', async () => {
    stubHub(() => false)
    killHub()

    await vi.advanceTimersByTimeAsync(1000)
    expect(hubConnectivity().attempt).toBe(1)
    await vi.advanceTimersByTimeAsync(1999)
    expect(hubConnectivity().attempt).toBe(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(hubConnectivity().attempt).toBe(2)
  })

  it('recovers from a request that got through before the next probe was due', async () => {
    stubHub(() => false)
    killHub()

    reportHubReachable()
    expect(hubConnectivity().status).toBe('online')

    await vi.advanceTimersByTimeAsync(15_000)
    expect(hubConnectivity().status).toBe('online')
  })

  it('drops a probe that rejected after something else reached the hub', async () => {
    let failProbe = () => {}
    vi.stubGlobal(
      'fetch',
      () =>
        new Promise<Response>((_, reject) => {
          failProbe = () => reject(new TypeError('failed to fetch'))
        }),
    )
    killHub()

    await vi.advanceTimersByTimeAsync(1000)
    expect(hubConnectivity().probing).toBe(true)

    reportHubReachable()
    failProbe()
    await vi.advanceTimersByTimeAsync(0)

    expect(hubConnectivity().status).toBe('online')
  })

  it('probes straight away on a manual retry', async () => {
    let listening = false
    stubHub(() => listening)
    killHub()

    listening = true
    retryHubNow()
    await vi.advanceTimersByTimeAsync(0)
    expect(hubConnectivity().status).toBe('online')
  })

  it('ignores a manual retry while the hub is up', () => {
    stubHub(() => true)
    retryHubNow()
    expect(hubConnectivity().status).toBe('online')
  })
})

describe('startHubWatch', () => {
  it('shows the screen at once when the boot probe finds nothing listening', async () => {
    stubHub(() => false)
    startHubWatch()
    await vi.advanceTimersByTimeAsync(0)
    expect(hubConnectivity().status).toBe('unreachable')
  })

  it('stays out of the way when the boot probe reaches the hub', async () => {
    stubHub(() => true)
    startHubWatch()
    await vi.advanceTimersByTimeAsync(0)
    expect(hubConnectivity().status).toBe('online')
  })
})
