import { describe, expect, it } from 'vitest'

import { createAndQueue, planInternalTicket } from './internal-ticket'
import type { InternalIssue, InternalIssueDraft } from './issues'
import type { EnqueueRequest, QueueResponse } from './queue'

function issue(over: Partial<InternalIssue> = {}): InternalIssue {
  return {
    repo: 'trau',
    id: 'LOOP-1',
    title: 'a chore',
    description: '',
    state: 'backlog',
    status: 'Backlog',
    labels: [],
    source: 'internal',
    has_children: false,
    ...over,
  }
}

function queueResponse(over: Partial<QueueResponse> = {}): QueueResponse {
  return { repo: 'trau', draining: false, items: [], ...over }
}

describe('planInternalTicket', () => {
  it('is a plain ticket when every sub-item row is empty', () => {
    expect(planInternalTicket('Fix flaky CI step', ['', ''])).toEqual({
      title: 'Fix flaky CI step',
      description: '',
      subs: [],
      isEpic: false,
    })
  })

  it('turns into an epic as soon as one sub-item row is filled', () => {
    const plan = planInternalTicket('Retire the legacy runner', [
      '',
      'Drop the shim',
    ])
    expect(plan).toEqual({
      title: 'Retire the legacy runner',
      description: '',
      subs: ['Drop the shim'],
      isEpic: true,
    })
  })

  it('keeps filled sub-items in row order and drops the blanks between them', () => {
    const plan = planInternalTicket('Spike', ['first', '   ', 'second', ''])
    expect(plan.subs).toEqual(['first', 'second'])
    expect(plan.isEpic).toBe(true)
  })

  it('trims the title and treats a whitespace-only one as absent', () => {
    expect(planInternalTicket('  Fix CI  ', []).title).toBe('Fix CI')
    expect(planInternalTicket('   ', []).title).toBe('')
  })

  it('does not count a whitespace-only sub-item row as an epic', () => {
    expect(planInternalTicket('Fix CI', ['   ']).isEpic).toBe(false)
  })

  it('trims the description and treats a whitespace-only one as absent', () => {
    expect(
      planInternalTicket('Fix CI', [], '  Full **context**  ').description,
    ).toBe('Full **context**')
    expect(planInternalTicket('Fix CI', [], '   ').description).toBe('')
    expect(planInternalTicket('Fix CI', []).description).toBe('')
  })
})

describe('createAndQueue', () => {
  function recorder() {
    const calls: string[] = []
    let n = 0
    const createIssue = async (draft: InternalIssueDraft) => {
      n++
      calls.push(
        `create ${draft.title}${draft.parent ? ` under ${draft.parent}` : ''}`,
      )
      return issue({
        id: `LOOP-${n}`,
        title: draft.title,
        parent: draft.parent,
      })
    }
    const enqueueOne = async (req: EnqueueRequest) => {
      calls.push(`enqueue ${req.id} as ${req.kind}`)
      return queueResponse()
    }
    return { calls, createIssue, enqueueOne }
  }

  it('files the parent, then each sub under it, then queues the epic', async () => {
    const { calls, createIssue, enqueueOne } = recorder()

    await createAndQueue(
      planInternalTicket('Retire the runner', [
        'Drop the shim',
        'Delete the flag',
      ]),
      createIssue,
      enqueueOne,
    )

    expect(calls).toEqual([
      'create Retire the runner',
      'create Drop the shim under LOOP-1',
      'create Delete the flag under LOOP-1',
      'enqueue LOOP-1 as epic',
    ])
  })

  it('queues a sub-less ticket as a ticket', async () => {
    const { calls, createIssue, enqueueOne } = recorder()

    await createAndQueue(
      planInternalTicket('Fix CI', ['']),
      createIssue,
      enqueueOne,
    )

    expect(calls).toEqual(['create Fix CI', 'enqueue LOOP-1 as ticket'])
  })

  it('puts the description on the parent draft only', async () => {
    const drafts: InternalIssueDraft[] = []

    await createAndQueue(
      planInternalTicket('Retire the runner', ['Drop the shim'], 'Why: **ci**'),
      async (draft) => {
        drafts.push(draft)
        return issue({ id: `LOOP-${drafts.length}`, title: draft.title })
      },
      async () => queueResponse(),
    )

    expect(drafts).toEqual([
      { title: 'Retire the runner', description: 'Why: **ci**' },
      { title: 'Drop the shim', parent: 'LOOP-1' },
    ])
  })

  it('omits the description from the draft when empty', async () => {
    const drafts: InternalIssueDraft[] = []

    await createAndQueue(
      planInternalTicket('Fix CI', [], '   '),
      async (draft) => {
        drafts.push(draft)
        return issue()
      },
      async () => queueResponse(),
    )

    expect(drafts[0]).not.toHaveProperty('description')
  })

  it('returns the queue the enqueue landed', async () => {
    const { createIssue } = recorder()
    const res = await createAndQueue(
      planInternalTicket('Fix CI', []),
      createIssue,
      async () => queueResponse({ draining: true }),
    )
    expect(res.draining).toBe(true)
  })

  it('reports a failed parent without filing subs or queueing', async () => {
    const calls: string[] = []
    const run = createAndQueue(
      planInternalTicket('Fix CI', ['a sub']),
      async () => {
        throw new Error('title is required')
      },
      async () => {
        calls.push('enqueue')
        return queueResponse()
      },
    )

    await expect(run).rejects.toThrow('title is required')
    expect(calls).toEqual([])
  })

  it('names the created parent and the row when a sub fails, and queues nothing', async () => {
    const calls: string[] = []
    const run = createAndQueue(
      planInternalTicket('Retire the runner', ['ok', 'boom']),
      async (draft) => {
        if (draft.title === 'boom') throw new Error('store is full')
        calls.push(`create ${draft.title}`)
        return issue({ id: 'LOOP-1', title: draft.title })
      },
      async () => {
        calls.push('enqueue')
        return queueResponse()
      },
    )

    await expect(run).rejects.toThrow(
      'LOOP-1 was created, but sub-item 2 failed: store is full',
    )
    await expect(run).rejects.toThrow('Nothing was queued')
    expect(calls).toEqual(['create Retire the runner', 'create ok'])
  })

  it('distinguishes created-but-not-queued from a sub failure', async () => {
    const { createIssue } = recorder()
    const run = createAndQueue(
      planInternalTicket('Fix CI', []),
      createIssue,
      async () => {
        throw new Error('repo is observe-only')
      },
    )

    await expect(run).rejects.toThrow(
      'LOOP-1 was created, but not queued: repo is observe-only',
    )
    await expect(run).rejects.toThrow('queue it by id')
  })

  it('reports a non-Error rejection as text', async () => {
    const { createIssue } = recorder()
    const run = createAndQueue(
      planInternalTicket('Fix CI', []),
      createIssue,
      async () => {
        throw 'boom'
      },
    )
    await expect(run).rejects.toThrow(
      'LOOP-1 was created, but not queued: boom',
    )
  })
})
