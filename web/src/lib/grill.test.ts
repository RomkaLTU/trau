import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { type Assignee } from './assignee'
import {
  abandonIssueSessions,
  activeFixSessionForIssue,
  activeSessionForIssue,
  applyGrill,
  applySessionModel,
  awaitingBreakdown,
  awaitingGrillsQueryKey,
  awaitingWithOpen,
  awaitingWithout,
  canCompose,
  composerPlaceholder,
  diffHasChanges,
  diffLines,
  dropAwaiting,
  grillAppliedOutcome,
  grillBanner,
  grillProgress,
  grillReducer,
  grillSessionsQueryOptions,
  isActiveSessionConflict,
  isAwaitingAnswer,
  isAutoAnswer,
  isGrillable,
  isOver,
  isSettled,
  lastAnswer,
  mergeMessages,
  NO_ACTIVITY,
  NO_REPLY,
  outcomePayload,
  pendingQuestion,
  publishGrillSession,
  questionPayload,
  researchGrillSessionsQueryOptions,
  setGrillAutoAccept,
  sortAwaiting,
  startGrillSession,
  stopGrill,
  upsertMessage,
  type DiffLine,
  type GrillActivity,
  type GrillAwaitingResponse,
  type GrillDelta,
  type GrillListResponse,
  type GrillLive,
  type GrillMessage,
  type GrillSession,
  type GrillState,
} from './grill'

function session(over: Partial<GrillSession>): GrillSession {
  return {
    id: '1',
    repo: 'loop',
    issue_id: 'COD-1',
    state: 'running',
    created_at: '2026-07-14T10:00:00Z',
    updated_at: '2026-07-14T10:00:00Z',
    ...over,
  }
}

function msg(over: Partial<GrillMessage>): GrillMessage {
  return {
    id: '1',
    role: 'agent',
    kind: 'info',
    payload: {},
    created_at: '2026-07-14T10:00:00Z',
    ...over,
  }
}

function question(id: string, over: Partial<GrillMessage['payload']> = {}) {
  return msg({
    id,
    role: 'agent',
    kind: 'question',
    payload: { text: 'Q' + id, ...over },
  })
}

function answer(id: string, text = 'A') {
  return msg({ id, role: 'user', kind: 'answer', payload: { text } })
}

function interjection(id: string, text = 'A') {
  return msg({ id, role: 'user', kind: 'interjection', payload: { text } })
}

describe('label gating', () => {
  it('qualifies issues carrying a triage label', () => {
    expect(isGrillable(['needs-triage'])).toBe(true)
    expect(isGrillable(['ready-for-agent', 'needs-info'])).toBe(true)
    expect(isGrillable(['ready-for-agent'])).toBe(false)
    expect(isGrillable([])).toBe(false)
  })
})

describe('state predicates', () => {
  it('treats applied and abandoned as settled', () => {
    expect(isSettled('applied')).toBe(true)
    expect(isSettled('abandoned')).toBe(true)
    expect(isSettled('parked')).toBe(false)
  })

  it('awaits an answer only in waiting, parked, and stalled', () => {
    const awaits: GrillState[] = ['waiting', 'parked', 'stalled']
    const idle: GrillState[] = ['running', 'finished', 'applied', 'abandoned']
    for (const s of awaits) expect(isAwaitingAnswer(s)).toBe(true)
    for (const s of idle) expect(isAwaitingAnswer(s)).toBe(false)
  })

  it('is over once finished or settled, but not while the interviewer works', () => {
    const over: GrillState[] = ['finished', 'applied', 'abandoned']
    const live: GrillState[] = ['running', 'waiting', 'parked', 'stalled']
    for (const s of over) expect(isOver(s)).toBe(true)
    for (const s of live) expect(isOver(s)).toBe(false)
  })
})

describe('activeSessionForIssue', () => {
  it('picks the newest unsettled session for the issue', () => {
    const sessions = [
      session({ id: '3', issue_id: 'COD-1', state: 'parked' }),
      session({ id: '2', issue_id: 'COD-1', state: 'applied' }),
      session({ id: '1', issue_id: 'COD-1', state: 'abandoned' }),
    ]
    expect(activeSessionForIssue(sessions, 'COD-1')?.id).toBe('3')
  })

  it('ignores other issues and settled-only histories', () => {
    const sessions = [
      session({ id: '4', issue_id: 'COD-2', state: 'waiting' }),
      session({ id: '1', issue_id: 'COD-1', state: 'applied' }),
    ]
    expect(activeSessionForIssue(sessions, 'COD-1')).toBeUndefined()
    expect(activeSessionForIssue(undefined, 'COD-1')).toBeUndefined()
  })
})

describe('activeFixSessionForIssue', () => {
  it('picks the live fix session, not the interview running beside it', () => {
    const sessions = [
      session({ id: '3', issue_id: 'COD-1', mode: 'interview', state: 'parked' }),
      session({ id: '2', issue_id: 'COD-1', mode: 'fix', state: 'running' }),
      session({ id: '1', issue_id: 'COD-2', mode: 'fix', state: 'waiting' }),
    ]
    expect(activeFixSessionForIssue(sessions, 'COD-1')?.id).toBe('2')
  })

  it('ignores a settled fix session, so the ticket can be diagnosed again', () => {
    const sessions = [session({ id: '1', issue_id: 'COD-1', mode: 'fix', state: 'applied' })]
    expect(activeFixSessionForIssue(sessions, 'COD-1')).toBeUndefined()
    expect(activeFixSessionForIssue(undefined, 'COD-1')).toBeUndefined()
  })
})

describe('sortAwaiting', () => {
  it('leads with the live question, then latest activity', () => {
    const sessions = [
      session({ id: '1', state: 'parked', updated_at: '2026-07-14T12:00:00Z' }),
      session({ id: '2', state: 'waiting', updated_at: '2026-07-14T10:00:00Z' }),
      session({ id: '3', state: 'stalled', updated_at: '2026-07-14T13:00:00Z' }),
      session({ id: '4', state: 'waiting', updated_at: '2026-07-14T11:00:00Z' }),
    ]
    expect(sortAwaiting(sessions).map((s) => s.id)).toEqual(['4', '2', '3', '1'])
  })

  it('leaves the input untouched and sorts an undated session last', () => {
    const sessions = [
      session({ id: '1', state: 'waiting', updated_at: '' }),
      session({ id: '2', state: 'waiting', updated_at: '2026-07-14T10:00:00Z' }),
    ]
    expect(sortAwaiting(sessions).map((s) => s.id)).toEqual(['2', '1'])
    expect(sessions.map((s) => s.id)).toEqual(['1', '2'])
  })
})

describe('awaitingWithOpen', () => {
  it('offers the feed as it stands while the open session still awaits', () => {
    const sessions = [session({ id: '1', state: 'waiting' }), session({ id: '2', state: 'parked' })]
    expect(awaitingWithOpen(sessions, sessions[1]).map((s) => s.id)).toEqual(['1', '2'])
  })

  it('carries the open session once answering drops it off the feed', () => {
    const open = session({ id: '9', state: 'running' })
    const sessions = [session({ id: '1', state: 'waiting' })]
    expect(awaitingWithOpen(sessions, open).map((s) => s.id)).toEqual(['9', '1'])
  })
})

describe('awaitingWithout', () => {
  it('drops the abandoned session and leaves the rest of the feed alone', () => {
    const sessions = [session({ id: '1' }), session({ id: '2' }), session({ id: '3' })]
    expect(awaitingWithout(sessions, '2').map((s) => s.id)).toEqual(['1', '3'])
    expect(awaitingWithout(sessions, '9').map((s) => s.id)).toEqual(['1', '2', '3'])
    expect(sessions).toHaveLength(3)
  })
})

describe('dropAwaiting', () => {
  const feed = (client: QueryClient) =>
    client.getQueryData<GrillAwaitingResponse>(awaitingGrillsQueryKey)

  it('takes the answered session off the cached feed rather than waiting on the poll', () => {
    const client = new QueryClient()
    client.setQueryData<GrillAwaitingResponse>(awaitingGrillsQueryKey, {
      sessions: [session({ id: '1', state: 'waiting' }), session({ id: '2', state: 'parked' })],
    })
    dropAwaiting(client, '1')
    expect(feed(client)?.sessions.map((s) => s.id)).toEqual(['2'])
  })

  it('leaves a feed that has not loaded yet unseeded', () => {
    const client = new QueryClient()
    dropAwaiting(client, '1')
    expect(feed(client)).toBeUndefined()
  })
})

describe('publishGrillSession', () => {
  const list = (client: QueryClient) =>
    client.getQueryData<GrillListResponse>(['grill', 'loop'])

  it('heads the cached list with the started session', () => {
    const client = new QueryClient()
    client.setQueryData<GrillListResponse>(['grill', 'loop'], {
      repo: 'loop',
      sessions: [session({ id: '1', issue_id: 'COD-1' })],
    })
    publishGrillSession(client, 'loop', session({ id: '2', issue_id: 'COD-2' }))
    expect(list(client)?.sessions.map((s) => s.id)).toEqual(['2', '1'])
  })

  it('replaces the cached copy of a session it already holds', () => {
    const client = new QueryClient()
    client.setQueryData<GrillListResponse>(['grill', 'loop'], {
      repo: 'loop',
      sessions: [session({ id: '1', state: 'running' })],
    })
    publishGrillSession(client, 'loop', session({ id: '1', state: 'waiting' }))
    expect(list(client)?.sessions).toEqual([session({ id: '1', state: 'waiting' })])
  })

  it('seeds a list that has not loaded yet, so the row is there on arrival', () => {
    const client = new QueryClient()
    publishGrillSession(client, 'loop', session({ id: '1' }))
    expect(list(client)).toEqual({ repo: 'loop', sessions: [session({ id: '1' })] })
  })
})

describe('grillSessionsQueryOptions', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('narrows the triage feed to interviews, so a research session stays off the queue', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ repo: 'loop', sessions: [] }),
    } as Response)
    vi.stubGlobal('fetch', fetchMock)

    await grillSessionsQueryOptions('loop').queryFn?.({} as never)

    expect((fetchMock.mock.calls[0] as [string])[0]).toBe(
      '/api/v1/repos/loop/grill?mode=interview',
    )
  })

  it('names research when the research feed fails without a reason', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ ok: false, status: 500, json: async () => ({}) } as Response)
    vi.stubGlobal('fetch', fetchMock)

    const err = await Promise.resolve(
      researchGrillSessionsQueryOptions('loop').queryFn?.({} as never),
    ).catch((e: unknown) => e)

    expect((err as Error).message).toBe('list research sessions failed: 500')
  })
})

describe('awaitingBreakdown', () => {
  it('counts each blocking state in the order the feed ranks them', () => {
    const sessions = [
      session({ id: '1', state: 'stalled' }),
      session({ id: '2', state: 'waiting' }),
      session({ id: '3', state: 'stalled' }),
      session({ id: '4', state: 'parked' }),
    ]
    expect(awaitingBreakdown(sessions)).toBe('1 waiting · 1 parked · 2 stalled')
  })

  it('names only the states present, and nothing on an empty feed', () => {
    expect(awaitingBreakdown([session({ state: 'waiting' }), session({ state: 'waiting' })])).toBe(
      '2 waiting',
    )
    expect(awaitingBreakdown([])).toBe('')
  })

  it('leads with the session the dock holds while the interviewer works', () => {
    const sessions = [
      session({ id: '9', state: 'running' }),
      session({ id: '1', state: 'waiting' }),
      session({ id: '2', state: 'stalled' }),
    ]
    expect(awaitingBreakdown(sessions)).toBe('1 thinking · 1 waiting · 1 stalled')
  })
})

describe('abandonIssueSessions', () => {
  it("settles the issue's unsettled sessions and leaves everything else alone", () => {
    const sessions = [
      session({ id: '1', issue_id: 'COD-1', state: 'waiting' }),
      session({ id: '2', issue_id: 'COD-1', state: 'applied' }),
      session({ id: '3', issue_id: 'COD-2', state: 'running' }),
      session({ id: '4', issue_id: undefined, state: 'waiting' }),
    ]
    const out = abandonIssueSessions(sessions, 'COD-1')
    expect(out.map((s) => s.state)).toEqual(['abandoned', 'applied', 'running', 'waiting'])
    expect(activeSessionForIssue(out, 'COD-1')).toBeUndefined()
    expect(sessions[0].state).toBe('waiting')
  })

  it('handles a missing list', () => {
    expect(abandonIssueSessions(undefined, 'COD-1')).toEqual([])
  })
})

describe('applySessionModel', () => {
  it('repoints only the switched session, so Start over reads the new model', () => {
    const sessions = [
      session({ id: '1', issue_id: 'COD-1', model: 'sonnet' }),
      session({ id: '2', issue_id: 'COD-2', model: 'sonnet' }),
    ]
    const out = applySessionModel(sessions, '1', 'haiku')
    expect(out.map((s) => s.model)).toEqual(['haiku', 'sonnet'])
    expect(sessions[0].model).toBe('sonnet')
  })

  it('handles a missing list', () => {
    expect(applySessionModel(undefined, '1', 'haiku')).toEqual([])
  })
})

describe('grillAppliedOutcome', () => {
  it('names the issue the apply anchored the session to', () => {
    const res = {
      session: session({
        state: 'applied',
        issue_id: 'COD-9',
        issue_title: 'Filed title',
      }),
      applied: true,
      steps: [],
    }
    expect(grillAppliedOutcome(res, 'create', 'Edited title')).toEqual({
      disposition: 'create',
      issueId: 'COD-9',
      issueTitle: 'Filed title',
      hasFailures: false,
    })
  })

  it('flags an apply that landed with a step that did not', () => {
    const res = {
      session: session({ state: 'applied' as const, issue_id: 'COD-9' }),
      applied: true,
      steps: [
        { step: 'issue: Dark mode', status: 'ok' as const },
        { step: 'assign: COD-9', status: 'failed' as const, error: 'not a member of the team' },
      ],
    }
    expect(grillAppliedOutcome(res, 'create', 'Dark mode').hasFailures).toBe(true)
  })

  it('falls back to the filed title when the join is empty', () => {
    const res = {
      session: session({
        state: 'applied',
        issue_id: 'COD-9',
        issue_title: '',
      }),
      applied: true,
      steps: [],
    }
    expect(grillAppliedOutcome(res, 'create', 'Edited title').issueTitle).toBe('Edited title')
  })

  it('reads an unanchored session as no issue', () => {
    const res = {
      session: session({ state: 'applied', issue_id: undefined }),
      applied: true,
      steps: [],
    }
    expect(grillAppliedOutcome(res, 'no_change', '').issueId).toBe('')
  })
})

describe('upsertMessage / mergeMessages', () => {
  it('inserts in id order', () => {
    let list: GrillMessage[] = []
    list = upsertMessage(list, msg({ id: '2' }))
    list = upsertMessage(list, msg({ id: '1' }))
    list = upsertMessage(list, msg({ id: '10' }))
    expect(list.map((m) => m.id)).toEqual(['1', '2', '10'])
  })

  it('replaces an existing id without duplicating', () => {
    const first = mergeMessages([], [msg({ id: '1', kind: 'question' }), answer('2')])
    const merged = mergeMessages(first, [answer('2', 'edited')])
    expect(merged).toHaveLength(2)
    expect(answerFor(merged, '2')).toBe('edited')
  })

  it('is a no-op when the same message reference is re-applied', () => {
    const m = msg({ id: '1' })
    const list = [m]
    expect(upsertMessage(list, m)).toBe(list)
  })
})

describe('pendingQuestion', () => {
  it('returns the last unanswered question', () => {
    const list = [question('1'), answer('2'), question('3')]
    expect(pendingQuestion(list)?.id).toBe('3')
  })

  it('is null once the latest question is answered', () => {
    const list = [question('1'), answer('2')]
    expect(pendingQuestion(list)).toBeNull()
  })

  it('is null when there are no questions (crash-parked)', () => {
    expect(pendingQuestion([msg({ id: '1', kind: 'info' })])).toBeNull()
  })
})

describe('grillProgress', () => {
  it('leaves a pending question outstanding', () => {
    expect(grillProgress([question('1'), answer('2'), question('3')])).toEqual({
      answered: 1,
      total: 2,
    })
  })

  it('counts every question once the last one is answered', () => {
    expect(grillProgress([question('1'), answer('2')])).toEqual({
      answered: 1,
      total: 1,
    })
  })

  // A stalled session resumes on a bare answer, so answers can outnumber questions.
  it('never counts more answers than were asked for', () => {
    expect(grillProgress([question('1'), answer('2'), answer('3')])).toEqual({
      answered: 1,
      total: 1,
    })
  })

  it('counts nothing on an untouched session', () => {
    expect(grillProgress([])).toEqual({ answered: 0, total: 0 })
  })
})

describe('questionPayload', () => {
  it('defaults allow_free_text to true and options to empty', () => {
    const p = questionPayload(question('1'))
    expect(p.allow_free_text).toBe(true)
    expect(p.options).toEqual([])
    expect(p.recommended).toBeUndefined()
    expect(p.why).toBeUndefined()
  })

  it('reads options and an explicit allow_free_text=false', () => {
    const p = questionPayload(question('1', { options: ['a', 'b'], allow_free_text: false }))
    expect(p.options).toEqual(['a', 'b'])
    expect(p.allow_free_text).toBe(false)
  })

  it('reads a recommended option and its why line', () => {
    const p = questionPayload(
      question('1', {
        options: ['a', 'b'],
        recommended: 'a',
        why: 'a is simpler',
      }),
    )
    expect(p.recommended).toBe('a')
    expect(p.why).toBe('a is simpler')
  })
})

describe('isAutoAnswer', () => {
  it('marks only an answer the hub took from the recommendation', () => {
    expect(isAutoAnswer(answer('1'))).toBe(false)
    expect(
      isAutoAnswer(msg({ id: '2', role: 'user', kind: 'answer', payload: { text: 'a', auto: true } })),
    ).toBe(true)
  })
})

describe('outcomePayload', () => {
  it('leaves sub_issues undefined for a rewrite', () => {
    const p = outcomePayload(
      msg({
        kind: 'outcome',
        payload: {
          disposition: 'rewrite',
          proposed_description: 'x',
          summary: 's',
        },
      }),
    )
    expect(p.disposition).toBe('rewrite')
    expect(p.sub_issues).toBeUndefined()
  })

  it('parses a split proposal with labels and blocked_by', () => {
    const p = outcomePayload(
      msg({
        kind: 'outcome',
        payload: {
          disposition: 'split',
          proposed_description: 'epic',
          summary: 's',
          sub_issues: [
            { title: 'A', description: 'da' },
            {
              title: 'B',
              description: 'db',
              labels: ['ready-for-agent'],
              blocked_by: [0],
            },
          ],
        },
      }),
    )
    expect(p.disposition).toBe('split')
    expect(p.sub_issues).toHaveLength(2)
    expect(p.sub_issues?.[1]).toEqual({
      title: 'B',
      description: 'db',
      labels: ['ready-for-agent'],
      blocked_by: [0],
    })
  })

  it('parses a research proposal carrying its report', () => {
    const p = outcomePayload(
      msg({
        kind: 'outcome',
        payload: {
          disposition: 'research',
          findings: '## Conclusion\n\nUse the vendor SDK.',
          summary: 's',
        },
      }),
    )
    expect(p.disposition).toBe('research')
    expect(p.findings).toBe('## Conclusion\n\nUse the vendor SDK.')
    expect(p.proposed_description).toBeUndefined()
  })

  it('leaves findings undefined when the outcome carries none', () => {
    const p = outcomePayload(
      msg({
        kind: 'outcome',
        payload: { disposition: 'rewrite', proposed_description: 'x', summary: 's' },
      }),
    )
    expect(p.findings).toBeUndefined()
  })

  it('coerces malformed sub_issue fields to safe defaults', () => {
    const p = outcomePayload(
      msg({
        kind: 'outcome',
        payload: {
          disposition: 'split',
          summary: 's',
          sub_issues: [{ title: 42, blocked_by: ['x', 1] }],
        },
      }),
    )
    expect(p.sub_issues?.[0].title).toBe('')
    expect(p.sub_issues?.[0].description).toBe('')
    expect(p.sub_issues?.[0].blocked_by).toEqual([1])
  })

  it('parses a single-issue create with title and labels', () => {
    const p = outcomePayload(
      msg({
        kind: 'outcome',
        payload: {
          disposition: 'create',
          title: 'Add dark mode',
          proposed_description: 'toggle in settings',
          labels: ['ready-for-agent', 'frontend'],
          summary: 's',
        },
      }),
    )
    expect(p.disposition).toBe('create')
    expect(p.title).toBe('Add dark mode')
    expect(p.labels).toEqual(['ready-for-agent', 'frontend'])
    expect(p.sub_issues).toBeUndefined()
  })

  it('parses a create-epic proposal with a breakdown', () => {
    const p = outcomePayload(
      msg({
        kind: 'outcome',
        payload: {
          disposition: 'create',
          title: 'Checkout redesign',
          proposed_description: 'epic',
          summary: 's',
          sub_issues: [
            { title: 'Cart', description: 'rebuild cart' },
            { title: 'Payment', description: 'wire payment', blocked_by: [0] },
          ],
        },
      }),
    )
    expect(p.title).toBe('Checkout redesign')
    expect(p.sub_issues).toHaveLength(2)
    expect(p.sub_issues?.[1].blocked_by).toEqual([0])
  })
})

describe('grillReducer', () => {
  const initial: GrillLive = {
    session: session({ state: 'running' }),
    live: false,
    hydrated: false,
    messages: [],
    pending: [],
    streaming: NO_REPLY,
    activity: NO_ACTIVITY,
  }

  it('hydrate seeds messages and adopts the session while not yet live', () => {
    const next = grillReducer(initial, {
      type: 'hydrate',
      detail: {
        session: session({ state: 'waiting' }),
        messages: [question('1')],
      },
    })
    expect(next.session.state).toBe('waiting')
    expect(next.messages.map((m) => m.id)).toEqual(['1'])
  })

  it('hydrated only turns on once the transcript lands', () => {
    expect(grillReducer(initial, { type: 'message', message: question('5') }).hydrated).toBe(false)
    const next = grillReducer(initial, {
      type: 'hydrate',
      detail: { session: session({ state: 'running' }), messages: [] },
    })
    expect(next.hydrated).toBe(true)
  })

  it('a stream state frame wins over a later hydrate', () => {
    const live = grillReducer(initial, {
      type: 'state',
      session: session({ state: 'finished' }),
    })
    expect(live.live).toBe(true)
    const hydrated = grillReducer(live, {
      type: 'hydrate',
      detail: { session: session({ state: 'running' }), messages: [] },
    })
    expect(hydrated.session.state).toBe('finished')
  })

  it('message upserts into the thread', () => {
    const next = grillReducer(initial, {
      type: 'message',
      message: question('5'),
    })
    expect(next.messages.map((m) => m.id)).toEqual(['5'])
  })
})

describe('streaming deltas', () => {
  const initial: GrillLive = {
    session: session({ state: 'running' }),
    live: false,
    hydrated: true,
    messages: [],
    pending: [],
    streaming: NO_REPLY,
    activity: NO_ACTIVITY,
  }

  const stream = (state: GrillLive, ...deltas: GrillDelta[]) =>
    deltas.reduce((s, delta) => grillReducer(s, { type: 'delta', delta }), state)

  it('accumulates chunks into the running turn’s reply', () => {
    const next = stream(initial, { seq: 1, text: 'Let me ' }, { seq: 2, text: 'push back.' })
    expect(next.streaming).toEqual({
      seq: 2,
      text: 'Let me push back.',
      holed: false,
    })
  })

  it('the stored message retires the streamed preview', () => {
    const streamed = stream(initial, { seq: 1, text: 'Let me push back.' })
    const next = grillReducer(streamed, {
      type: 'message',
      message: question('7'),
    })
    expect(next.streaming).toEqual(NO_REPLY)
    expect(next.messages.map((m) => m.id)).toEqual(['7'])
  })

  it('a state frame ends the turn’s stream and rebases the seq', () => {
    const streamed = stream(initial, { seq: 1, text: 'half a thou' })
    const settled = grillReducer(streamed, {
      type: 'state',
      session: session({ state: 'waiting' }),
    })
    expect(settled.streaming).toEqual(NO_REPLY)

    // The next turn's deltas number from one again, so they must read as contiguous.
    const resumed = grillReducer(settled, {
      type: 'state',
      session: session({ state: 'running' }),
    })
    expect(stream(resumed, { seq: 1, text: 'Next turn.' }).streaming.text).toBe('Next turn.')
  })

  it('a dropped chunk holes the reply for the rest of the turn', () => {
    const next = stream(
      initial,
      { seq: 1, text: 'Let me ' },
      { seq: 4, text: 'back.' },
      { seq: 5, text: ' Why?' },
    )
    expect(next.streaming.holed).toBe(true)
    // The text after a gap is never spliced onto the text before it.
    expect(next.streaming.text).not.toContain('Let me ')
  })

  it('ignores deltas trailing a settled turn', () => {
    const settled: GrillLive = {
      ...initial,
      session: session({ state: 'finished' }),
    }
    expect(stream(settled, { seq: 1, text: 'too late' }).streaming).toEqual(NO_REPLY)
  })

  it('leaves a hub that streams nothing on the message-at-a-time flow', () => {
    const next = grillReducer(initial, {
      type: 'message',
      message: question('1'),
    })
    expect(next.streaming).toEqual(NO_REPLY)
    expect(next.messages.map((m) => m.id)).toEqual(['1'])
  })

  // The user typing mid-turn settles nothing: the reply beside it is still being
  // written, so the preview has to survive the bubble landing in the thread.
  it('an interjection joins the thread without ending the reply', () => {
    const streamed = stream(initial, { seq: 1, text: 'Let me ' })
    const next = grillReducer(streamed, {
      type: 'message',
      message: interjection('7', 'skip the schema'),
    })
    expect(next.streaming).toEqual({ seq: 1, text: 'Let me ', holed: false })
    expect(next.messages.map((m) => m.id)).toEqual(['7'])
    expect(stream(next, { seq: 2, text: 'push back.' }).streaming.text).toBe('Let me push back.')
  })
})

describe('streaming activity', () => {
  const initial: GrillLive = {
    session: session({ state: 'running' }),
    live: false,
    hydrated: true,
    messages: [],
    pending: [],
    streaming: NO_REPLY,
    activity: NO_ACTIVITY,
  }

  const tool = (seq: number, name: string, id?: string): GrillActivity => ({
    seq,
    kind: 'tool',
    name,
    ...(id ? { id } : {}),
  })

  const result = (seq: number, ok: boolean, id?: string): GrillActivity => ({
    seq,
    kind: 'result',
    ok,
    ...(id ? { id } : {}),
  })

  const report = (state: GrillLive, ...frames: GrillActivity[]) =>
    frames.reduce((s, activity) => grillReducer(s, { type: 'activity', activity }), state)

  it('appends frames in the order the turn reported them', () => {
    const next = report(initial, tool(1, 'Read'), { seq: 2, kind: 'thinking' })
    expect(next.activity.items.map((a) => a.kind)).toEqual(['tool', 'thinking'])
    expect(next.activity.seq).toBe(2)
  })

  it('the call’s detail fills in the row it already opened', () => {
    const next = report(
      initial,
      tool(1, 'Bash', 'toolu_1'),
      { seq: 2, kind: 'tool', id: 'toolu_1', name: 'Bash', detail: 'go test ./...' },
    )
    expect(next.activity.items).toHaveLength(1)
    expect(next.activity.items[0]).toEqual({
      seq: 1,
      kind: 'tool',
      id: 'toolu_1',
      name: 'Bash',
      detail: 'go test ./...',
    })
  })

  it('a result resolves the row it names, leaving the others running', () => {
    const next = report(
      initial,
      tool(1, 'Bash', 'toolu_1'),
      tool(2, 'Grep', 'toolu_2'),
      result(3, false, 'toolu_2'),
    )
    expect(next.activity.items.map((a) => a.ok)).toEqual([undefined, false])
  })

  it('a result with no id settles the oldest row still running', () => {
    const next = report(initial, tool(1, 'shell'), tool(2, 'web_search'), result(3, true))
    expect(next.activity.items.map((a) => a.ok)).toEqual([true, undefined])
  })

  it('a result whose row has aged out of the ring settles nothing', () => {
    const next = report(initial, tool(1, 'Bash', 'toolu_1'), result(2, true, 'toolu_9'))
    expect(next.activity.items.map((a) => a.ok)).toEqual([undefined])
  })

  const thinking = (seq: number, text?: string): GrillActivity => ({
    seq,
    kind: 'thinking',
    ...(text ? { text } : {}),
  })

  it('a stretch grows as the thinking behind it is written', () => {
    const next = report(initial, thinking(1), thinking(2, 'weighing '), thinking(3, 'it up'))
    expect(next.activity.items).toHaveLength(1)
    expect(next.activity.items[0]).toEqual({ seq: 1, kind: 'thinking', text: 'weighing it up' })
  })

  it('a stretch marker starts a row rather than growing the last one', () => {
    const next = report(initial, thinking(1), thinking(2, 'first'), thinking(3), thinking(4, 'second'))
    expect(next.activity.items.map((a) => a.text)).toEqual(['first', 'second'])
  })

  it('thinking landing with no stretch open opens one', () => {
    const next = report(initial, tool(1, 'Read'), thinking(2, 'weighing it'))
    expect(next.activity.items.map((a) => a.kind)).toEqual(['tool', 'thinking'])
    expect(next.activity.items[1].text).toBe('weighing it')
  })

  it('grows the stretch a later tool row sits above', () => {
    const next = report(initial, thinking(1, 'weighing '), tool(2, 'Read'), thinking(3, 'it up'))
    expect(next.activity.items.map((a) => a.kind)).toEqual(['thinking', 'tool'])
    expect(next.activity.items[0].text).toBe('weighing it up')
  })

  it('keeps the tail of a stretch that runs long', () => {
    const next = report(initial, thinking(1), thinking(2, 'x'.repeat(1990)), thinking(3, 'y'.repeat(30)))
    const text = next.activity.items[0].text ?? ''
    expect(text).toHaveLength(2000)
    expect(text.endsWith('y'.repeat(30))).toBe(true)
    expect(text.startsWith('x'.repeat(1970))).toBe(true)
  })

  it('a stretch that carries no text stays a bare row', () => {
    const next = report(initial, thinking(1))
    expect(next.activity.items).toEqual([{ seq: 1, kind: 'thinking' }])
  })

  it('a tool already back is born resolved', () => {
    const next = report(initial, { seq: 1, kind: 'tool', name: 'read_file', ok: true })
    expect(next.activity.items[0].ok).toBe(true)
  })

  it('keeps only the last 50 rows', () => {
    const frames = Array.from({ length: 60 }, (_, i) => tool(i + 1, `T${i + 1}`))
    const next = report(initial, ...frames)
    expect(next.activity.items).toHaveLength(50)
    expect(next.activity.items[0].name).toBe('T11')
    expect(next.activity.items[49].name).toBe('T60')
  })

  it('the turn’s message clears the ring', () => {
    const reported = report(initial, tool(1, 'Read'))
    const next = grillReducer(reported, { type: 'message', message: question('7') })
    expect(next.activity).toEqual(NO_ACTIVITY)
  })

  it('a state frame clears the ring and rebases the seq', () => {
    const reported = report(initial, tool(1, 'Read'))
    const settled = grillReducer(reported, {
      type: 'state',
      session: session({ state: 'waiting' }),
    })
    expect(settled.activity).toEqual(NO_ACTIVITY)

    // The next turn's frames number from one again, so they must read as contiguous.
    const resumed = grillReducer(settled, {
      type: 'state',
      session: session({ state: 'running' }),
    })
    expect(report(resumed, tool(1, 'Grep')).activity.items).toHaveLength(1)
  })

  it('a dropped frame clears the ring rather than leaving a hole in it', () => {
    const holed = report(initial, tool(1, 'Read'), tool(2, 'Grep'), tool(5, 'Edit'))
    expect(holed.activity.items).toEqual([])
    expect(holed.activity.seq).toBe(5)
    expect(report(holed, tool(6, 'Bash')).activity.items.map((a) => a.name)).toEqual(['Bash'])
  })

  it('ignores frames trailing a settled turn', () => {
    const settled: GrillLive = { ...initial, session: session({ state: 'finished' }) }
    expect(report(settled, tool(1, 'Read')).activity).toEqual(NO_ACTIVITY)
  })
})

describe('optimistic send', () => {
  const initial: GrillLive = {
    session: session({ state: 'waiting' }),
    live: false,
    hydrated: true,
    messages: [],
    pending: [],
    streaming: NO_REPLY,
    activity: NO_ACTIVITY,
  }

  const sent = (text = 'A') => grillReducer(initial, { type: 'send', id: 'p1', text })

  it('holds the answer as pending until the hub echoes it', () => {
    expect(sent().pending).toEqual([{ id: 'p1', text: 'A', failed: false }])
  })

  it('the echo retires the optimistic twin, leaving only the real message', () => {
    const next = grillReducer(sent(), {
      type: 'message',
      message: answer('7', 'A'),
    })
    expect(next.pending).toEqual([])
    expect(next.messages.map((m) => m.id)).toEqual(['7'])
  })

  it('the echo of an interjection retires its twin too', () => {
    const running = grillReducer(
      { ...initial, session: session({ state: 'running' }) },
      { type: 'send', id: 'p1', text: 'A' },
    )
    const next = grillReducer(running, {
      type: 'message',
      message: interjection('7', 'A'),
    })
    expect(next.pending).toEqual([])
    expect(next.messages.map((m) => m.id)).toEqual(['7'])
  })

  it('an echo of different text leaves the pending answer alone', () => {
    const next = grillReducer(sent(), {
      type: 'message',
      message: answer('7', 'other'),
    })
    expect(next.pending).toHaveLength(1)
  })

  it('retires one twin per echo when the same text was sent twice', () => {
    const twice = grillReducer(sent(), { type: 'send', id: 'p2', text: 'A' })
    const next = grillReducer(twice, {
      type: 'message',
      message: answer('7', 'A'),
    })
    expect(next.pending.map((p) => p.id)).toEqual(['p2'])
  })

  // The hub answers a send twice: once in the POST response, once over the stream.
  it('retires one twin for an echo the POST and the stream both deliver', () => {
    const twice = grillReducer(sent(), { type: 'send', id: 'p2', text: 'A' })
    const posted = grillReducer(twice, {
      type: 'message',
      message: answer('7', 'A'),
    })
    const streamed = grillReducer(posted, {
      type: 'message',
      message: answer('7', 'A'),
    })
    expect(streamed.pending.map((p) => p.id)).toEqual(['p2'])
    expect(streamed.messages.map((m) => m.id)).toEqual(['7'])
  })

  it('a hydrate backfill retires twins the stream already echoed', () => {
    const next = grillReducer(sent(), {
      type: 'hydrate',
      detail: {
        session: session({ state: 'waiting' }),
        messages: [answer('7', 'A')],
      },
    })
    expect(next.pending).toEqual([])
  })

  // A refetch re-hydrates the whole transcript, old turns included.
  it('a re-hydrate of an already-held answer leaves an in-flight twin alone', () => {
    const backfill = {
      type: 'hydrate' as const,
      detail: {
        session: session({ state: 'waiting' }),
        messages: [answer('7', 'A')],
      },
    }
    const held = grillReducer(initial, backfill)
    const again = grillReducer(grillReducer(held, { type: 'send', id: 'p1', text: 'A' }), backfill)
    expect(again.pending.map((p) => p.id)).toEqual(['p1'])
  })

  it('a failed send keeps its text for retry and is not retired by an echo', () => {
    const failed = grillReducer(sent(), {
      type: 'send-failed',
      id: 'p1',
      text: 'A',
    })
    expect(failed.pending[0].failed).toBe(true)
    const echoed = grillReducer(failed, {
      type: 'message',
      message: answer('7', 'A'),
    })
    expect(echoed.pending.map((p) => p.id)).toEqual(['p1'])
  })

  it('surfaces the failure on a surviving twin when the echo retired its own entry', () => {
    const twice = grillReducer(sent(), { type: 'send', id: 'p2', text: 'A' })
    const echoed = grillReducer(twice, {
      type: 'message',
      message: answer('7', 'A'),
    })
    const failed = grillReducer(echoed, {
      type: 'send-failed',
      id: 'p1',
      text: 'A',
    })
    expect(failed.pending).toEqual([{ id: 'p2', text: 'A', failed: true }])
  })

  it('retry clears the failure so the next echo retires it', () => {
    const failed = grillReducer(sent(), {
      type: 'send-failed',
      id: 'p1',
      text: 'A',
    })
    const again = grillReducer(failed, { type: 'send-retry', id: 'p1' })
    expect(again.pending[0].failed).toBe(false)
    const echoed = grillReducer(again, {
      type: 'message',
      message: answer('7', 'A'),
    })
    expect(echoed.pending).toEqual([])
  })

  it('discard drops the send outright', () => {
    const failed = grillReducer(sent(), {
      type: 'send-failed',
      id: 'p1',
      text: 'A',
    })
    expect(grillReducer(failed, { type: 'send-discard', id: 'p1' }).pending).toEqual([])
  })
})

describe('composer gating', () => {
  it('takes typing only in the states that can accept an answer', () => {
    expect(canCompose('waiting')).toBe(true)
    expect(canCompose('parked')).toBe(true)
    expect(canCompose('finished')).toBe(false)
  })

  it('shuts the box on a stalled session — its Resume button carries the answer', () => {
    expect(isAwaitingAnswer('stalled')).toBe(true)
    expect(canCompose('stalled')).toBe(false)
    expect(composerPlaceholder('stalled')).toBe('Session stalled — resume to keep answering…')
  })

  it('takes typing mid-turn as steering rather than as an answer', () => {
    expect(canCompose('running')).toBe(true)
    expect(composerPlaceholder('running')).toBe(
      'Steer the agent — it will see this at its next step…',
    )
  })
})

describe('lastAnswer', () => {
  it('is the most recent answer — the resume pre-fill', () => {
    expect(lastAnswer([answer('1', 'first'), question('2'), answer('3', 'second')])).toBe('second')
  })

  it('is empty when the session stalled before any answer', () => {
    expect(lastAnswer([question('1')])).toBe('')
  })
})

describe('grillBanner', () => {
  it('has no banner while waiting (the question card carries it)', () => {
    expect(grillBanner(session({ state: 'waiting' }))).toBeNull()
  })

  it('shows a thinking banner while running', () => {
    expect(grillBanner(session({ state: 'running' }))?.tone).toBe('thinking')
  })

  it('parks with the idle hint when there is no reason', () => {
    const b = grillBanner(session({ state: 'parked', parked_reason: '' }))
    expect(b?.tone).toBe('parked')
    expect(b?.hint).toMatch(/pick up anytime/i)
  })

  it('parks with the stored reason when one is present', () => {
    const b = grillBanner(session({ state: 'parked', parked_reason: 'agent stopped unexpectedly' }))
    expect(b?.hint).toBe('agent stopped unexpectedly')
  })

  it('offers resume on a stalled session and surfaces the cause', () => {
    const b = grillBanner(session({ state: 'stalled', parked_reason: 'needs re-authentication' }))
    expect(b?.showResume).toBe(true)
    expect(b?.hint).toBe('needs re-authentication')
  })

  // The headline used to name a usage/rate limit for every stall, so an auth wall
  // was reported as a limit the user had to wait out rather than a login to fix.
  it('does not attribute a stall to a rate limit the reason does not mention', () => {
    const b = grillBanner(session({ state: 'stalled', parked_reason: 'needs re-authentication' }))
    expect(b?.headline).not.toMatch(/rate|usage|limit/i)
    expect(b?.headline).toBe('Session stalled')
  })

  it('still leads with the stall when no reason was stored', () => {
    const b = grillBanner(session({ state: 'stalled', parked_reason: '' }))
    expect(b?.headline).toBe('Session stalled')
    expect(b?.hint).toMatch(/resume/i)
  })

  it('reports the applied outcome', () => {
    expect(grillBanner(session({ state: 'applied' }))?.tone).toBe('applied')
  })
})

describe('diffLines', () => {
  it('marks every line equal when nothing changed', () => {
    const lines = diffLines('a\nb\nc', 'a\nb\nc')
    expect(lines.map((l) => l.op)).toEqual(['equal', 'equal', 'equal'])
    expect(diffHasChanges(lines)).toBe(false)
  })

  it('keeps context and pairs an edit as delete then insert', () => {
    const lines = diffLines('a\nb\nc', 'a\nB\nc')
    expect(compact(lines)).toEqual(['equal a', 'delete b', 'insert B', 'equal c'])
    expect(diffHasChanges(lines)).toBe(true)
  })

  it('reports an inserted line without deleting surrounding context', () => {
    const lines = diffLines('a\nc', 'a\nb\nc')
    expect(compact(lines)).toEqual(['equal a', 'insert b', 'equal c'])
  })

  it('reports a deleted line', () => {
    const lines = diffLines('a\nb\nc', 'a\nc')
    expect(compact(lines)).toEqual(['equal a', 'delete b', 'equal c'])
  })

  it('treats an empty original as all inserts and an empty replacement as all deletes', () => {
    expect(diffLines('', 'x\ny').map((l) => l.op)).toEqual(['insert', 'insert'])
    expect(diffLines('x\ny', '').map((l) => l.op)).toEqual(['delete', 'delete'])
    expect(diffLines('', '')).toEqual([])
  })

  it('normalises CRLF so a line-ending-only change is not a diff', () => {
    expect(diffHasChanges(diffLines('a\r\nb', 'a\nb'))).toBe(false)
  })
})

describe('applyGrill', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function stubApply() {
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 200, json: async () => ({}) } as Response)
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }

  function applyBody(fetchMock: ReturnType<typeof stubApply>): unknown {
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    return JSON.parse(init.body as string)
  }

  it('carries the picked person so every created issue lands on them', async () => {
    const ada: Assignee = { id: 'usr_1', name: 'Ada Lovelace', me: false }
    const fetchMock = stubApply()

    await applyGrill('7', 'body', undefined, 'Dark mode', undefined, ada)

    expect((fetchMock.mock.calls[0] as [string])[0]).toBe('/api/v1/grill/7/apply')
    expect(applyBody(fetchMock)).toEqual({
      proposed_description: 'body',
      title: 'Dark mode',
      assignee: { id: 'usr_1', name: 'Ada Lovelace' },
    })
  })

  it('omits the assignee when nobody was picked', async () => {
    const fetchMock = stubApply()

    await applyGrill('7', 'body', undefined, 'Dark mode')

    expect(applyBody(fetchMock)).toEqual({
      proposed_description: 'body',
      title: 'Dark mode',
    })
  })
})

describe('startGrillSession', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function stubStart(status: number, body: unknown) {
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ ok: status < 400, status, json: async () => body } as Response)
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }

  it('rides the focus note in as the opening idea', async () => {
    const fetchMock = stubStart(200, session({ id: '1' }))

    await startGrillSession('loop', 'COD-1', { seed: 'why two flows?' })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/repos/loop/grill')
    expect(JSON.parse(init.body as string)).toEqual({
      issue_id: 'COD-1',
      idea: 'why two flows?',
      model: '',
      provider: '',
      mode: 'interview',
      auto_accept: false,
    })
  })

  it('asks for auto-accepted recommendations when the checkbox is on', async () => {
    const fetchMock = stubStart(200, session({ id: '1' }))

    await startGrillSession('loop', 'COD-1', { seed: 'why two flows?', autoAccept: true })

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toMatchObject({ auto_accept: true })
  })

  it('declares the research session type when one is picked', async () => {
    const fetchMock = stubStart(200, session({ id: '1' }))

    await startGrillSession('loop', 'COD-1', {
      seed: 'which oauth flow?',
      mode: 'research',
    })

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toMatchObject({ mode: 'research' })
  })

  it('marks a refused start as a conflict when a session raced it', async () => {
    stubStart(409, { error: 'COD-1 already has an active grill session' })

    const err = await startGrillSession('loop', 'COD-1').catch((e: unknown) => e)

    expect((err as Error).message).toBe('COD-1 already has an active grill session')
    expect(isActiveSessionConflict(err)).toBe(true)
  })

  it('leaves every other refusal a plain failure', async () => {
    stubStart(500, { error: 'no interviewer configured' })

    const err = await startGrillSession('loop', 'COD-1').catch((e: unknown) => e)

    expect((err as Error).message).toBe('no interviewer configured')
    expect(isActiveSessionConflict(err)).toBe(false)
  })

  it('names the research session type when a refusal carries no reason', async () => {
    stubStart(503, {})

    const err = await startGrillSession('loop', '', {
      seed: 'which oauth flow?',
      mode: 'research',
    }).catch((e: unknown) => e)

    expect((err as Error).message).toBe('start research session failed: 503')
  })
})

describe('setGrillAutoAccept', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('switches the live session and hands back the updated view', async () => {
    const updated = session({ id: '7', auto_accept: true })
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 200, json: async () => updated } as Response)
    vi.stubGlobal('fetch', fetchMock)

    expect(await setGrillAutoAccept('7', true)).toEqual(updated)

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/grill/7/auto-accept')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ enabled: true })
  })
})

describe('stopGrill', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('parks the running session and hands back the stopped view', async () => {
    const parked = session({
      id: '4',
      state: 'parked',
      parked_reason: 'you stopped the agent — your next message steers the conversation',
    })
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 200, json: async () => parked } as Response)
    vi.stubGlobal('fetch', fetchMock)

    expect(await stopGrill('4')).toEqual(parked)

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/grill/4/stop')
    expect(init.method).toBe('POST')
  })

  it('raises the hub refusal when the session has no turn to stop', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 409,
        json: async () => ({ error: 'no turn is running' }),
      } as Response),
    )

    await expect(stopGrill('4')).rejects.toThrow('no turn is running')
  })
})

function compact(lines: DiffLine[]): string[] {
  return lines.map((l) => `${l.op} ${l.text}`)
}

function answerFor(list: GrillMessage[], id: string): string {
  const found = list.find((m) => m.id === id)
  const payload = (found?.payload ?? {}) as { text?: string }
  return payload.text ?? ''
}
