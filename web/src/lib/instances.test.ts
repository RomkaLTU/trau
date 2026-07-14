import { afterEach, describe, expect, it, vi } from 'vitest'

import { CheckpointError } from './checkpoints'
import { startInstance } from './instances'

function respond(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('startInstance', () => {
  it('flags a 409 live-conflict as a CheckpointError the run views can branch on', async () => {
    const message = 'salonradar already has a live loop (pid 42) — stop it before starting another run in the same working tree'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond(409, { error: message, live: true })))

    const error = await startInstance({ repo: 'salonradar', ticket: 'COD-1' }).catch((e) => e)

    expect(error).toBeInstanceOf(CheckpointError)
    expect(error.live).toBe(true)
    expect(error.message).toBe(message)
  })

  it('leaves a non-live failure unflagged so it stays an inline notice', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(respond(403, { error: 'observe-only repo' })))

    const error = await startInstance({ repo: 'salonradar', ticket: 'COD-1' }).catch((e) => e)

    expect(error).toBeInstanceOf(CheckpointError)
    expect(error.live).toBe(false)
    expect(error.message).toBe('observe-only repo')
  })
})
