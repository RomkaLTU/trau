import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  parseSSEBlock,
  splitSSE,
  streamSSE,
  type SSEMessage,
} from './sse'
import { clearServeToken, onUnauthorized, setServeToken } from './auth'

function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(enc.encode(chunk))
      controller.close()
    },
  })
}

afterEach(() => {
  clearServeToken()
  vi.unstubAllGlobals()
})

describe('splitSSE', () => {
  it('splits complete frames and keeps the trailing partial', () => {
    const { blocks, rest } = splitSSE(
      'id: 1\ndata: a\n\nevent: meta\ndata: b\n\ndata: par',
    )
    expect(blocks).toEqual(['id: 1\ndata: a', 'event: meta\ndata: b'])
    expect(rest).toBe('data: par')
  })

  it('handles CRLF frame boundaries', () => {
    const { blocks, rest } = splitSSE('data: a\r\n\r\ndata: b')
    expect(blocks).toEqual(['data: a'])
    expect(rest).toBe('data: b')
  })
})

describe('parseSSEBlock', () => {
  it('defaults the event type to message and reads the id', () => {
    expect(parseSSEBlock('id: 7\ndata: {"a":1}')).toEqual({
      event: 'message',
      data: '{"a":1}',
      id: '7',
    })
  })

  it('reads a named event', () => {
    expect(parseSSEBlock('event: meta\ndata: {}')).toEqual({
      event: 'meta',
      data: '{}',
      id: undefined,
    })
  })

  it('joins multi-line data with newlines', () => {
    expect(parseSSEBlock('data: one\ndata: two')?.data).toBe('one\ntwo')
  })

  it('drops comment-only keep-alives', () => {
    expect(parseSSEBlock(': ping')).toBeNull()
  })
})

describe('streamSSE', () => {
  it('attaches the token and dispatches parsed frames', async () => {
    setServeToken('tok')
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      ok: true,
      body: streamOf([
        'id: 1\ndata: {"n":1}\n\n',
        'event: meta\ndata: {"id":"x"}\n\n',
      ]),
    } as unknown as Response)
    vi.stubGlobal('fetch', fetchMock)

    const messages: SSEMessage[] = []
    const opened = vi.fn()
    await new Promise<void>((resolve) => {
      const close = streamSSE('/api/v1/events/stream', {
        onOpen: opened,
        onMessage: (msg) => {
          messages.push(msg)
          if (messages.length === 2) {
            close()
            resolve()
          }
        },
      })
    })

    const headers = new Headers(
      (fetchMock.mock.calls[0][1] as RequestInit).headers,
    )
    expect(headers.get('Authorization')).toBe('Bearer tok')
    expect(headers.get('Accept')).toBe('text/event-stream')
    expect(opened).toHaveBeenCalled()
    expect(messages).toEqual([
      { event: 'message', data: '{"n":1}', id: '1' },
      { event: 'meta', data: '{"id":"x"}', id: undefined },
    ])
  })

  it('treats 401 as fatal: raises unauthorized and does not reconnect', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ status: 401, ok: false, body: null } as Response)
    vi.stubGlobal('fetch', fetchMock)
    const seen = vi.fn()
    const off = onUnauthorized(seen)
    const errored = vi.fn()

    await new Promise<void>((resolve) => {
      streamSSE('/api/v1/events/stream', {
        onMessage: () => {},
        onError: () => {
          errored()
          resolve()
        },
      })
    })

    expect(seen).toHaveBeenCalledOnce()
    expect(errored).toHaveBeenCalledOnce()
    expect(fetchMock).toHaveBeenCalledOnce()
    off()
  })
})
