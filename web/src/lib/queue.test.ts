import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { apiFetch } from './api'
import {
  batchDisplayName,
  batchName,
  batchSelectable,
  batchStartBlocker,
  batchSummary,
  createBatch,
  dismissBatch,
  enqueueFresh,
  promoteQueueItem,
  publishQueue,
  queueActiveIds,
  queueCounts,
  queueCoveredIds,
  queueExecutable,
  queueLive,
  queueQueryOptions,
  queueRunnable,
  releaseGateLabel,
  requeueIssue,
  runNext,
  runOnly,
  skipResumeApplies,
  spawnHoldReason,
  startBatch,
  stopQueue,
  updateBatch,
  type QueueBatch,
  type QueueItem,
  type QueueResponse,
} from './queue'
import type { Run } from './runs'
import { builderView } from './timeline'

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
  return { repo: 'trau', draining: false, stopping: false, items: [], ...over }
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

  it('promotes a queued paused item to the front, then arms the drain', async () => {
    mockFetch
      .mockResolvedValueOnce(response(409, { error: 'COD-2 is already in the queue' }))
      .mockResolvedValueOnce(
        response(
          200,
          queueResponse({
            items: [
              item({ id: 'COD-1', status: 'pending' }),
              item({ id: 'COD-2', status: 'paused' }),
            ],
          }),
        ),
      )
      .mockResolvedValueOnce(response(200, queueResponse()))
      .mockResolvedValueOnce(response(200, queueResponse({ draining: true })))

    const res = await runNext('trau', { id: 'COD-2' })

    expect(mockFetch).toHaveBeenNthCalledWith(
      3,
      '/api/v1/repos/trau/queue/COD-2/move',
      expect.objectContaining({ body: JSON.stringify({ to: 'front' }) }),
    )
    expect(mockFetch).toHaveBeenNthCalledWith(
      4,
      '/api/v1/repos/trau/queue/drain',
      expect.objectContaining({ body: JSON.stringify({ draining: true }) }),
    )
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

describe('runOnly', () => {
  const drainCalls = () =>
    mockFetch.mock.calls.filter(([url]) => String(url).endsWith('/drain'))

  it('appends an unqueued item, then runs it without arming the drain', async () => {
    mockFetch
      .mockResolvedValueOnce(response(201, queueResponse()))
      .mockResolvedValueOnce(
        response(200, queueResponse({ items: [item({ status: 'running' })] })),
      )

    const res = await runOnly('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenNthCalledWith(
      1,
      '/api/v1/repos/trau/queue',
      expect.objectContaining({ body: JSON.stringify({ id: 'COD-1' }) }),
    )
    expect(mockFetch).toHaveBeenNthCalledWith(
      2,
      '/api/v1/repos/trau/queue/COD-1/run',
      { method: 'POST' },
    )
    expect(drainCalls()).toHaveLength(0)
    expect(res.items[0].status).toBe('running')
  })

  it('runs an item the queue already holds where it stands', async () => {
    mockFetch
      .mockResolvedValueOnce(response(409, { error: 'COD-1 is already in the queue' }))
      .mockResolvedValueOnce(
        response(200, queueResponse({ items: [item({ status: 'paused' })] })),
      )
      .mockResolvedValueOnce(response(200, queueResponse()))

    await runOnly('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenCalledTimes(3)
    expect(mockFetch).toHaveBeenNthCalledWith(
      3,
      '/api/v1/repos/trau/queue/COD-1/run',
      { method: 'POST' },
    )
  })

  it('drops a settled leftover and re-queues it before running', async () => {
    mockFetch
      .mockResolvedValueOnce(response(409, { error: 'COD-1 is already in the queue' }))
      .mockResolvedValueOnce(
        response(200, queueResponse({ items: [item({ status: 'done' })] })),
      )
      .mockResolvedValueOnce(response(200, queueResponse()))
      .mockResolvedValueOnce(response(201, queueResponse()))
      .mockResolvedValueOnce(response(200, queueResponse()))

    await runOnly('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenNthCalledWith(3, '/api/v1/repos/trau/queue/COD-1', {
      method: 'DELETE',
    })
    expect(mockFetch).toHaveBeenNthCalledWith(
      4,
      '/api/v1/repos/trau/queue',
      expect.objectContaining({ body: JSON.stringify({ id: 'COD-1' }) }),
    )
    expect(mockFetch).toHaveBeenNthCalledWith(
      5,
      '/api/v1/repos/trau/queue/COD-1/run',
      { method: 'POST' },
    )
  })

  it('surfaces the enqueue error for an id the queue cannot take', async () => {
    mockFetch
      .mockResolvedValueOnce(response(404, { error: 'unknown ticket' }))
      .mockResolvedValueOnce(response(200, queueResponse()))

    await expect(runOnly('trau', { id: 'COD-404' })).rejects.toThrow(
      'unknown ticket',
    )
    expect(
      mockFetch.mock.calls.filter(([url]) => String(url).endsWith('/run')),
    ).toHaveLength(0)
  })

  it('surfaces a run refusal, such as a blocker added since render', async () => {
    mockFetch
      .mockResolvedValueOnce(response(201, queueResponse()))
      .mockResolvedValueOnce(response(409, { error: 'COD-1 is blocked by COD-2' }))

    await expect(runOnly('trau', { id: 'COD-1' })).rejects.toThrow(
      'COD-1 is blocked by COD-2',
    )
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

describe('queueRunnable', () => {
  it('is false for an empty queue', () => {
    expect(queueRunnable([])).toBe(false)
  })

  it('is false when every item has settled', () => {
    expect(
      queueRunnable([
        item({ id: 'COD-1', status: 'done' }),
        item({ id: 'COD-2', status: 'failed' }),
        item({ id: 'COD-3', status: 'skipped' }),
      ]),
    ).toBe(false)
  })

  it('is true while a pending item remains among settled ones', () => {
    expect(
      queueRunnable([
        item({ id: 'COD-1', status: 'done' }),
        item({ id: 'COD-2', status: 'pending' }),
      ]),
    ).toBe(true)
  })

  it('is true for a paused epic whose sub-issues all read done', () => {
    expect(
      queueRunnable([
        item({
          id: 'COD-9',
          kind: 'epic',
          status: 'paused',
          reason: 'epic COD-9 unfinalized — waiting on COD-11',
          sub_issues: [
            { id: 'COD-10', title: 'a', state: 'done' },
            { id: 'COD-11', title: 'b', state: 'done' },
          ],
        }),
      ]),
    ).toBe(true)
  })
})

describe('queueLive', () => {
  it('is false for an idle queue and true while it drains', () => {
    expect(queueLive(queueResponse())).toBe(false)
    expect(queueLive(queueResponse({ draining: true }))).toBe(true)
  })

  it('stays true through a stop, from the ack to the parked row', () => {
    const running = item({ id: 'COD-1', status: 'running' })
    expect(queueLive(queueResponse({ stopping: true, items: [running] }))).toBe(true)
    expect(queueLive(queueResponse({ items: [running] }))).toBe(true)
    expect(
      queueLive(queueResponse({ items: [item({ id: 'COD-1', status: 'paused' })] })),
    ).toBe(false)
  })
})

describe('requeueIssue', () => {
  it('posts to the issue requeue route and answers with the repaired queue', async () => {
    const repaired = queueResponse({
      items: [item({ id: 'COD-1', status: 'pending' })],
    })
    mockFetch.mockResolvedValueOnce(response(200, repaired))

    expect(await requeueIssue('trau', 'COD-1')).toEqual(repaired)
    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/issues/COD-1/requeue',
      { method: 'POST' },
    )
  })

  it('surfaces a refusal from the hub', async () => {
    mockFetch.mockResolvedValueOnce(
      response(409, { error: 'trau has a loop running — stop it before requeuing COD-1' }),
    )

    await expect(requeueIssue('trau', 'COD-1')).rejects.toThrow(
      'trau has a loop running',
    )
  })
})

describe('stopQueue', () => {
  it('posts to the stop route and answers with the queue', async () => {
    const stopped = queueResponse({
      stopping: true,
      items: [item({ id: 'COD-1', status: 'running' })],
    })
    mockFetch.mockResolvedValueOnce(response(202, stopped))

    expect(await stopQueue('trau')).toEqual(stopped)
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/repos/trau/queue/stop', {
      method: 'POST',
    })
  })

  it('surfaces a refusal from the hub', async () => {
    mockFetch.mockResolvedValueOnce(response(403, { error: 'repo "trau" is observe-only' }))

    await expect(stopQueue('trau')).rejects.toThrow('repo "trau" is observe-only')
  })
})

// The Loop card gates Start on the builder list, which drops settled rows, so a
// queue that holds nothing but history reads as unrunnable there too.
describe('the Loop Start gate', () => {
  const disabled = (items: QueueItem[]) =>
    !queueRunnable(builderView(items, []).queue)

  it('is disabled for an empty queue', () => {
    expect(disabled([])).toBe(true)
  })

  it('is disabled when every row has settled', () => {
    expect(
      disabled([
        item({ id: 'COD-1', status: 'done' }),
        item({ id: 'COD-2', status: 'skipped' }),
      ]),
    ).toBe(true)
  })

  it('is enabled for a pending or paused row', () => {
    expect(disabled([item({ id: 'COD-1', status: 'pending' })])).toBe(false)
    expect(disabled([item({ id: 'COD-1', status: 'paused' })])).toBe(false)
  })
})

describe('queueCoveredIds', () => {
  it('covers nothing for an empty queue', () => {
    expect(queueCoveredIds([])).toEqual(new Set())
  })

  it('covers each queued item id regardless of status', () => {
    expect(
      queueCoveredIds([
        item({ id: 'COD-1' }),
        item({ id: 'COD-2', status: 'done' }),
      ]),
    ).toEqual(new Set(['COD-1', 'COD-2']))
  })

  it('covers the sub-issues captured under a queued epic', () => {
    expect(
      queueCoveredIds([
        item({
          id: 'COD-2',
          kind: 'epic',
          sub_issues: [
            { id: 'COD-3', title: 'a', state: 'todo' },
            { id: 'COD-4', title: 'b', state: 'done' },
          ],
        }),
      ]),
    ).toEqual(new Set(['COD-2', 'COD-3', 'COD-4']))
  })
})

describe('queueActiveIds', () => {
  it.each(['done', 'failed', 'skipped', 'awaiting-merge'])(
    'drops a %s ticket row',
    (status) => {
      expect(
        queueActiveIds([item({ id: 'COD-1' }), item({ id: 'COD-2', status })]),
      ).toEqual(new Set(['COD-1']))
    },
  )

  it('drops a settled epic along with its sub-issues', () => {
    expect(
      queueActiveIds([
        item({
          id: 'COD-2',
          kind: 'epic',
          status: 'done',
          sub_issues: [
            { id: 'COD-3', title: 'a', state: 'done' },
            { id: 'COD-4', title: 'b', state: 'done' },
          ],
        }),
      ]),
    ).toEqual(new Set())
  })

  it.each(['pending', 'running', 'paused'])(
    'keeps a %s epic and every sub-issue under it',
    (status) => {
      expect(
        queueActiveIds([
          item({
            id: 'COD-2',
            kind: 'epic',
            status,
            sub_issues: [
              { id: 'COD-3', title: 'a', state: 'todo' },
              { id: 'COD-4', title: 'b', state: 'done' },
            ],
          }),
        ]),
      ).toEqual(new Set(['COD-2', 'COD-3', 'COD-4']))
    },
  )
})

describe('enqueueFresh', () => {
  it('adds the item with a single request when the id is free', async () => {
    mockFetch.mockResolvedValueOnce(response(201, queueResponse()))

    await enqueueFresh('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenCalledTimes(1)
    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/queue',
      expect.objectContaining({ body: JSON.stringify({ id: 'COD-1' }) }),
    )
  })

  it('drops a settled leftover and queues the item again', async () => {
    mockFetch
      .mockResolvedValueOnce(response(409, { error: 'COD-1 is already in the queue' }))
      .mockResolvedValueOnce(
        response(200, queueResponse({ items: [item({ id: 'COD-1', status: 'done' })] })),
      )
      .mockResolvedValueOnce(response(200, queueResponse()))
      .mockResolvedValueOnce(response(201, queueResponse({ items: [item({ id: 'COD-1' })] })))

    const res = await enqueueFresh('trau', { id: 'COD-1' })

    expect(mockFetch).toHaveBeenNthCalledWith(3, '/api/v1/repos/trau/queue/COD-1', {
      method: 'DELETE',
    })
    expect(res.items).toEqual([item({ id: 'COD-1' })])
  })

  it('rethrows when the conflicting row is still live', async () => {
    mockFetch
      .mockResolvedValueOnce(response(409, { error: 'COD-1 is already in the queue' }))
      .mockResolvedValueOnce(
        response(200, queueResponse({ items: [item({ id: 'COD-1', status: 'running' })] })),
      )

    await expect(enqueueFresh('trau', { id: 'COD-1' })).rejects.toThrow(
      'COD-1 is already in the queue',
    )
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })
})

describe('promoteQueueItem', () => {
  it('posts the front destination to the move endpoint', async () => {
    const promoted = queueResponse({
      draining: true,
      items: [item({ id: 'COD-2' }), item({ id: 'COD-1', position: 2 })],
    })
    mockFetch.mockResolvedValueOnce(response(200, promoted))

    const res = await promoteQueueItem('trau', 'COD-2')

    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/queue/COD-2/move',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ to: 'front' }),
      }),
    )
    expect(res).toEqual(promoted)
  })

  it('surfaces the hub refusal when the item is no longer pending', async () => {
    mockFetch.mockResolvedValueOnce(
      response(409, { error: 'COD-2 is running and cannot be reordered' }),
    )

    await expect(promoteQueueItem('trau', 'COD-2')).rejects.toThrow(
      'COD-2 is running and cannot be reordered',
    )
  })

  it('falls back to the status when the refusal carries no message', async () => {
    mockFetch.mockResolvedValueOnce(response(404, null))

    await expect(promoteQueueItem('trau', 'COD-9')).rejects.toThrow(
      'run next failed: 404',
    )
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

describe('batch mutations', () => {
  it('files the picked ids under a named batch', async () => {
    mockFetch.mockResolvedValueOnce(response(200, queueResponse()))

    await createBatch('trau', ['COD-1', 'COD-2'], 'API polish')

    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/queue/batches',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ name: 'API polish', ids: ['COD-1', 'COD-2'] }),
      }),
    )
  })

  it('leaves the name out when the batch is filed unnamed', async () => {
    mockFetch.mockResolvedValueOnce(response(200, queueResponse()))

    await createBatch('trau', ['COD-1'])

    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/queue/batches',
      expect.objectContaining({ body: JSON.stringify({ ids: ['COD-1'] }) }),
    )
  })

  it('reports a refusal of an id another batch already holds', async () => {
    mockFetch.mockResolvedValueOnce(
      response(409, { error: 'COD-1 already belongs to batch api-polish' }),
    )

    await expect(createBatch('trau', ['COD-1'])).rejects.toThrow(
      'COD-1 already belongs to batch api-polish',
    )
  })

  it('renames a batch in place', async () => {
    mockFetch.mockResolvedValueOnce(response(200, queueResponse()))

    await updateBatch('trau', 'api-polish', { name: 'API polish v2' })

    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/queue/batches/api-polish',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({ name: 'API polish v2' }),
      }),
    )
  })

  it('dismisses a batch without a body', async () => {
    mockFetch.mockResolvedValueOnce(response(200, queueResponse()))

    await dismissBatch('trau', 'api-polish')

    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/queue/batches/api-polish',
      { method: 'DELETE' },
    )
  })

  it('starts a batch with the run-level knobs the footer offers', async () => {
    mockFetch.mockResolvedValueOnce(
      response(200, queueResponse({ draining: true, draining_batch: 'api-polish' })),
    )

    const res = await startBatch('trau', 'api-polish', {
      no_resume: true,
      on_fault: 'skip',
    })

    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/repos/trau/queue/batches/api-polish/start',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ no_resume: true, on_fault: 'skip' }),
      }),
    )
    expect(res.draining_batch).toBe('api-polish')
  })

  it('surfaces a start refused by a blocker outside the batch', async () => {
    mockFetch.mockResolvedValueOnce(
      response(409, {
        error:
          'batch api-polish is blocked by COD-9 — ship them or take them into the batch',
      }),
    )

    await expect(startBatch('trau', 'api-polish')).rejects.toThrow(
      'batch api-polish is blocked by COD-9 — ship them or take them into the batch',
    )
  })
})

describe('batchDisplayName', () => {
  const batch = (over: Partial<QueueBatch>): QueueBatch => ({
    id: 'api-polish',
    name: '',
    created_at: '2026-08-01T14:32:00Z',
    ...over,
  })

  it('is the name the batch was filed under', () => {
    expect(batchDisplayName(batch({ name: 'API polish' }))).toBe('API polish')
  })

  it('falls back to when it was filed for an unnamed batch', () => {
    const stamp = new Date('2026-08-01T14:32:00Z').toLocaleString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    })
    expect(batchDisplayName(batch({}))).toBe(stamp)
  })

  it('labels a member row by id and stays empty for a batch the repo dropped', () => {
    const batches = [batch({ name: 'API polish' })]
    expect(batchName(batches, 'api-polish')).toBe('API polish')
    expect(batchName(batches, 'gone')).toBe('')
    expect(batchName(undefined, '')).toBe('')
  })
})

describe('batchSelectable', () => {
  it('takes a runnable row no batch holds yet', () => {
    expect(batchSelectable(item({ status: 'pending' }))).toBe(true)
    expect(batchSelectable(item({ status: 'paused' }))).toBe(true)
  })

  it('leaves out a settled row', () => {
    expect(batchSelectable(item({ status: 'done' }))).toBe(false)
    expect(batchSelectable(item({ status: 'running' }))).toBe(false)
  })

  it('leaves out a row another batch already holds', () => {
    expect(batchSelectable(item({ status: 'pending', batch: 'api-polish' }))).toBe(
      false,
    )
  })
})

describe('batchSummary', () => {
  const items = [
    item({ id: 'COD-1', status: 'done', batch: 'api-polish' }),
    item({ id: 'COD-2', status: 'pending', batch: 'api-polish' }),
    item({ id: 'COD-3', status: 'paused', batch: 'api-polish' }),
    item({ id: 'COD-4', status: 'pending' }),
  ]

  it('counts the members and what a Start would still launch', () => {
    const summary = batchSummary(items, 'api-polish')
    expect(summary.members).toBe(3)
    expect(summary.runnable).toBe(2)
  })

  it('tallies the outcomes and leaves pending out of them', () => {
    expect(batchSummary(items, 'api-polish').tally).toEqual([
      { status: 'done', count: 1 },
      { status: 'paused', count: 1 },
    ])
  })
})

describe('batchStartBlocker', () => {
  const items = [
    item({ id: 'COD-1', status: 'pending', batch: 'api-polish' }),
    item({ id: 'COD-2', status: 'pending' }),
  ]

  it('is empty for a runnable batch on an idle queue', () => {
    expect(
      batchStartBlocker(queueResponse({ items }), 'api-polish'),
    ).toBe('')
  })

  it('names the drain in flight', () => {
    expect(
      batchStartBlocker(queueResponse({ items, draining: true }), 'api-polish'),
    ).toBe('the queue is draining — stop it before starting a batch')
  })

  it('names the run already in flight', () => {
    expect(
      batchStartBlocker(
        queueResponse({
          items: [...items, item({ id: 'COD-3', status: 'running' })],
        }),
        'api-polish',
      ),
    ).toBe('COD-3 is already running')
  })

  it('refuses a fully settled batch, which keeps its card as a record', () => {
    expect(
      batchStartBlocker(
        queueResponse({
          items: [item({ id: 'COD-1', status: 'done', batch: 'api-polish' })],
        }),
        'api-polish',
      ),
    ).toBe('nothing left to run in this batch')
  })
})

describe('releaseGateLabel', () => {
  it('names the epic whose release holds the queue', () => {
    expect(
      releaseGateLabel(
        queueResponse({ draining: true, releasing_epic: 'COD-1442' }),
      ),
    ).toBe('waiting for COD-1442 to finish releasing')
  })

  it('is empty while nothing gates the queue', () => {
    expect(releaseGateLabel(queueResponse({ draining: true }))).toBe('')
  })

  it('is empty without a queue at all', () => {
    expect(releaseGateLabel()).toBe('')
  })
})

describe('spawnHoldReason', () => {
  it('names the gate an armed drain stopped at', () => {
    expect(
      spawnHoldReason(
        queueResponse({
          draining: true,
          held: true,
          held_reason: 'a loop is already running in this repo',
        }),
      ),
    ).toBe('a loop is already running in this repo')
  })

  it('still says the drain is holding when the hub sends no reason', () => {
    expect(
      spawnHoldReason(queueResponse({ draining: true, held: true })),
    ).toBe('the hub is holding the next spawn')
  })

  it('is empty while the drain is starting work normally', () => {
    expect(spawnHoldReason(queueResponse({ draining: true }))).toBe('')
    expect(spawnHoldReason()).toBe('')
  })
})
