import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  applicationServerKey,
  dropPushSubscription,
  fetchPushKey,
  savePushSubscription,
  sendTestPush,
  subscriptionPayload,
} from './push'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

afterEach(() => vi.unstubAllGlobals())

describe('applicationServerKey', () => {
  it('decodes an unpadded base64url key to its raw bytes', () => {
    const bytes = applicationServerKey('BQIDBA')
    expect(Array.from(bytes)).toEqual([5, 2, 3, 4])
  })

  it('decodes the base64url alphabet the VAPID key uses', () => {
    expect(Array.from(applicationServerKey('-_8'))).toEqual([251, 255])
  })

  it('decodes a full 65-byte P-256 point', () => {
    const raw = new Uint8Array(65).fill(7)
    raw[0] = 4
    const encoded = btoa(String.fromCharCode(...raw))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '')
    expect(Array.from(applicationServerKey(encoded))).toEqual(Array.from(raw))
  })
})

describe('subscriptionPayload', () => {
  it('lifts the endpoint and encryption keys out of toJSON', () => {
    const payload = subscriptionPayload({
      endpoint: 'https://push.example/abc',
      toJSON: () => ({
        endpoint: 'https://push.example/abc',
        keys: { p256dh: 'p256', auth: 'auth' },
      }),
    })
    expect(payload).toEqual({
      endpoint: 'https://push.example/abc',
      keys: { p256dh: 'p256', auth: 'auth' },
    })
  })

  it('falls back to empty keys when the browser reports none', () => {
    const payload = subscriptionPayload({
      endpoint: 'https://push.example/abc',
      toJSON: () => ({ endpoint: 'https://push.example/abc' }),
    })
    expect(payload.keys).toEqual({ p256dh: '', auth: '' })
  })
})

describe('push api', () => {
  it('reads the VAPID public key', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ public_key: 'BQID' })))
    await expect(fetchPushKey()).resolves.toBe('BQID')
  })

  it('posts the subscription and deletes it by endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ status: 'ok' }, 201))
    vi.stubGlobal('fetch', fetchMock)

    await savePushSubscription({
      endpoint: 'https://push.example/abc',
      keys: { p256dh: 'p256', auth: 'auth' },
    })
    await dropPushSubscription('https://push.example/abc')

    const [subscribeUrl, subscribeInit] = fetchMock.mock.calls[0]
    expect(subscribeUrl).toBe('/api/v1/push/subscriptions')
    expect(subscribeInit.method).toBe('POST')
    expect(JSON.parse(subscribeInit.body)).toEqual({
      endpoint: 'https://push.example/abc',
      keys: { p256dh: 'p256', auth: 'auth' },
    })

    const [dropUrl, dropInit] = fetchMock.mock.calls[1]
    expect(dropUrl).toBe('/api/v1/push/subscriptions')
    expect(dropInit.method).toBe('DELETE')
    expect(JSON.parse(dropInit.body)).toEqual({
      endpoint: 'https://push.example/abc',
    })
  })

  it('reports the hub error message when a send fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse({ error: 'no vapid identity' }, 500)),
    )
    await expect(sendTestPush()).rejects.toThrow('no vapid identity')
  })

  it('returns the delivery counts of a test send', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ sent: 2, pruned: 1 })))
    await expect(sendTestPush()).resolves.toEqual({ sent: 2, pruned: 1 })
  })
})
