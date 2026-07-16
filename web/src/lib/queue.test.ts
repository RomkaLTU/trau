import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiFetch } from './api'
import {
  publishQueue,
  queueCounts,
  queueExecutable,
  queueQueryOptions,
  runNext,
  skipResumeApplies,
  type QueueItem,
  type QueueResponse,
} from './queue'
import type { Run } from './runs'

vi.mock('./api', () => ({ apiFetch: vi.fn() }))

const mockFetch = vi.mocked(apiFetch)

afterEach(() => {
  mockFetch.mockReset()
})

function response(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

function item(over: Partial<QueueItem>): QueueItem {
  return {
    position: 1,
    kind: 'ticket',
    id: 'COD-1',
    status: 'pending',
    ...over,
  }
}

function queueResponse(over: Partial<QueueResponse> = {}): QueueResponse {
  return { repo: 'trau', draining: false, items: [], ...over }
}

function run(over: Partial<Run>): Run {
  return {
    ticket: 'COD-1',
    phase: 'implement',
    phase_rank: 0,
    terminal: false,
    ...over,
  }
}

describe('publishQueue', () => {
  const cached = (client: QueryClient, repo: string) =>
    client.getQueryData(queueQueryOptions(repo).queryKey)

  it('lands the response on the key the queue query reads', () => {
    const client = new QueryClient()
    const res = queueResponse({ items: [item({ id: 'COD-1' })] })
    publishQueue(client, 'trau', res)
    expect(cached(client, 'trau')).toEqual(res)
  })

  it('replaces the cached queue, so an added item shows without a refetch', () => {
    const client = new QueryClient()
    publishQueue(client, 'trau', queueResponse({ draining: true }))
    publishQueue(
      client,
      'trau',
      queueResponse({ draining: true, items: [item({ id: 'COD-2' })] }),
    )
    expect(cached(client, 'trau')).toEqual(
      queueResponse({ draining: true, items: [item({ id: 'COD-2' })] }),
    )
  })

  it('scopes the write to its own repo', () => {
    const client = new QueryClient()
    publishQueue(client, 'trau', queueResponse({ items: [item({ id: 'COD-1' })] }))
    expect(cached(client, 'salonradar')).toBeUndefined()
  })
})

describe('runNext', () => {
  const drainCalls = () =>
    mockFetch.mock.calls.filter(([url]) => String(url).endsWith('/drain'))

  it('front-inserts the item, then arms the drain', async () => {
    mockFetch
      .mockResolvedValueOnce(response(201, queueResponse()))
      .mockResolvedValueOnce(response(200, queueResponse({ draining: true })))

    const res = await runNext('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenNthCalledWith(
      1,
      '/api/v1/repos/trau/queue',
      expect.objectContaining({
        body: JSON.stringify({ id: 'COD-1', front: true }),
      }),
    )
    expect(mockFetch).toHaveBeenNthCalledWith(
      2,
      '/api/v1/repos/trau/queue/drain',
      expect.objectContaining({ body: JSON.stringify({ draining: true }) }),
    )
    expect(res.draining).toBe(true)
  })

  it('resumes a queued paused item by arming without re-queuing', async () => {
    mockFetch
      .mockResolvedValueOnce(response(409, { error: 'COD-1 is already in the queue' }))
      .mockResolvedValueOnce(
        response(200, queueResponse({ items: [item({ id: 'COD-1', status: 'paused' })] })),
      )
      .mockResolvedValueOnce(response(200, queueResponse({ draining: true })))

    const res = await runNext('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenCalledTimes(3)
    expect(res.draining).toBe(true)
  })

  it('drops a settled leftover and re-queues it before arming', async () => {
    mockFetch
      .mockResolvedValueOnce(response(409, { error: 'COD-1 is already in the queue' }))
      .mockResolvedValueOnce(
        response(200, queueResponse({ items: [item({ id: 'COD-1', status: 'failed' })] })),
      )
      .mockResolvedValueOnce(response(200, queueResponse()))
      .mockResolvedValueOnce(response(201, queueResponse()))
      .mockResolvedValueOnce(response(200, queueResponse({ draining: true })))

    await runNext('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenNthCalledWith(
      3,
      '/api/v1/repos/trau/queue/COD-1',
      { method: 'DELETE' },
    )
    expect(mockFetch).toHaveBeenNthCalledWith(
      4,
      '/api/v1/repos/trau/queue',
      expect.objectContaining({
        body: JSON.stringify({ id: 'COD-1', front: true }),
      }),
    )
  })

  it('does not arm the drain when the id is not queueable', async () => {
    mockFetch
      .mockResolvedValueOnce(response(404, { error: 'unknown ticket' }))
      .mockResolvedValueOnce(response(200, queueResponse()))

    await expect(runNext('trau', { id: 'COD-404' })).rejects.toThrow(
      'unknown ticket',
    )
    expect(drainCalls()).toHaveLength(0)
  })
})

describe('queueCounts', () => {
  it('counts an empty queue as all zeros', () => {
    expect(queueCounts([])).toEqual({ total: 0, tickets: 0, epics: 0 })
  })

  it('splits the total between tickets and epics', () => {
    const counts = queueCounts([
      item({ id: 'COD-1', kind: 'ticket' }),
      item({ id: 'COD-2', kind: 'epic' }),
      item({ id: 'COD-3', kind: 'ticket' }),
    ])
    expect(counts).toEqual({ total: 3, tickets: 2, epics: 1 })
  })
})

describe('queueExecutable', () => {
  it('counts each ticket once', () => {
    expect(
      queueExecutable([
        item({ id: 'COD-1', kind: 'ticket' }),
        item({ id: 'COD-2', kind: 'ticket' }),
      ]),
    ).toBe(2)
  })

  it('excludes settled tickets from the estimate', () => {
    expect(
      queueExecutable([
        item({ id: 'COD-1', kind: 'ticket', status: 'done' }),
        item({ id: 'COD-2', kind: 'ticket', status: 'failed' }),
        item({ id: 'COD-3', kind: 'ticket', status: 'skipped' }),
        item({ id: 'COD-4', kind: 'ticket', status: 'pending' }),
      ]),
    ).toBe(1)
  })

  it('counts an epic by its not-done sub-issues', () => {
    expect(
      queueExecutable([
        item({ id: 'COD-1', kind: 'ticket' }),
        item({
          id: 'COD-2',
          kind: 'epic',
          sub_issues: [
            { id: 'COD-3', title: 'a', state: 'todo' },
            { id: 'COD-4', title: 'b', state: 'done' },
            { id: 'COD-5', title: 'c', state: 'todo' },
          ],
        }),
      ]),
    ).toBe(3)
  })
})

describe('skipResumeApplies', () => {
  it('is false for an all-pending queue with no runs', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' }), item({ id: 'COD-2' })],
        [],
      ),
    ).toBe(false)
  })

  it.each(['done', 'failed', 'skipped', 'paused', 'running'])(
    'is true when an item has %s status',
    (status) => {
      expect(
        skipResumeApplies(
          [item({ id: 'COD-1' }), item({ id: 'COD-2', status })],
          [],
        ),
      ).toBe(true)
    },
  )

  it('is true when a non-terminal run matches a queued ticket id', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' })],
        [run({ ticket: 'COD-1', terminal: false })],
      ),
    ).toBe(true)
  })

  it('is true when a non-terminal run matches an epic sub-issue id', () => {
    expect(
      skipResumeApplies(
        [
          item({
            id: 'COD-2',
            kind: 'epic',
            sub_issues: [{ id: 'COD-3', title: 'a', state: 'todo' }],
          }),
        ],
        [run({ ticket: 'COD-3', terminal: false })],
      ),
    ).toBe(true)
  })

  it('is false when the only matching run is terminal', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' })],
        [run({ ticket: 'COD-1', terminal: true })],
      ),
    ).toBe(false)
  })

  it('is false when a non-terminal run is for an unrelated ticket', () => {
    expect(
      skipResumeApplies(
        [item({ id: 'COD-1' })],
        [run({ ticket: 'COD-99', terminal: false })],
      ),
    ).toBe(false)
  })
})
