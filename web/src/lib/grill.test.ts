import { describe, expect, it } from 'vitest'

import {
  activeSessionForIssue,
  canCompose,
  composerPlaceholder,
  diffHasChanges,
  diffLines,
  grillBanner,
  grillProgress,
  grillReducer,
  isAwaitingAnswer,
  isGrillable,
  isSettled,
  lastAnswer,
  mergeMessages,
  NO_REPLY,
  outcomePayload,
  pendingQuestion,
  questionPayload,
  upsertMessage,
  type DiffLine,
  type GrillDelta,
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
  return msg({ id, role: 'agent', kind: 'question', payload: { text: 'Q' + id, ...over } })
}

function answer(id: string, text = 'A') {
  return msg({ id, role: 'user', kind: 'answer', payload: { text } })
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
    expect(grillProgress([question('1'), answer('2')])).toEqual({ answered: 1, total: 1 })
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
  })

  it('reads options and an explicit allow_free_text=false', () => {
    const p = questionPayload(
      question('1', { options: ['a', 'b'], allow_free_text: false }),
    )
    expect(p.options).toEqual(['a', 'b'])
    expect(p.allow_free_text).toBe(false)
  })
})

describe('outcomePayload', () => {
  it('leaves sub_issues undefined for a rewrite', () => {
    const p = outcomePayload(
      msg({ kind: 'outcome', payload: { disposition: 'rewrite', proposed_description: 'x', summary: 's' } }),
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
            { title: 'B', description: 'db', labels: ['ready-for-agent'], blocked_by: [0] },
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
  }

  it('hydrate seeds messages and adopts the session while not yet live', () => {
    const next = grillReducer(initial, {
      type: 'hydrate',
      detail: { session: session({ state: 'waiting' }), messages: [question('1')] },
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
    const live = grillReducer(initial, { type: 'state', session: session({ state: 'finished' }) })
    expect(live.live).toBe(true)
    const hydrated = grillReducer(live, {
      type: 'hydrate',
      detail: { session: session({ state: 'running' }), messages: [] },
    })
    expect(hydrated.session.state).toBe('finished')
  })

  it('message upserts into the thread', () => {
    const next = grillReducer(initial, { type: 'message', message: question('5') })
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
    const next = grillReducer(streamed, { type: 'message', message: question('7') })
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
    const next = grillReducer(initial, { type: 'message', message: question('1') })
    expect(next.streaming).toEqual(NO_REPLY)
    expect(next.messages.map((m) => m.id)).toEqual(['1'])
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
  }

  const sent = (text = 'A') =>
    grillReducer(initial, { type: 'send', id: 'p1', text })

  it('holds the answer as pending until the hub echoes it', () => {
    expect(sent().pending).toEqual([{ id: 'p1', text: 'A', failed: false }])
  })

  it('the echo retires the optimistic twin, leaving only the real message', () => {
    const next = grillReducer(sent(), { type: 'message', message: answer('7', 'A') })
    expect(next.pending).toEqual([])
    expect(next.messages.map((m) => m.id)).toEqual(['7'])
  })

  it('an echo of different text leaves the pending answer alone', () => {
    const next = grillReducer(sent(), { type: 'message', message: answer('7', 'other') })
    expect(next.pending).toHaveLength(1)
  })

  it('retires one twin per echo when the same text was sent twice', () => {
    const twice = grillReducer(sent(), { type: 'send', id: 'p2', text: 'A' })
    const next = grillReducer(twice, { type: 'message', message: answer('7', 'A') })
    expect(next.pending.map((p) => p.id)).toEqual(['p2'])
  })

  // The hub answers a send twice: once in the POST response, once over the stream.
  it('retires one twin for an echo the POST and the stream both deliver', () => {
    const twice = grillReducer(sent(), { type: 'send', id: 'p2', text: 'A' })
    const posted = grillReducer(twice, { type: 'message', message: answer('7', 'A') })
    const streamed = grillReducer(posted, { type: 'message', message: answer('7', 'A') })
    expect(streamed.pending.map((p) => p.id)).toEqual(['p2'])
    expect(streamed.messages.map((m) => m.id)).toEqual(['7'])
  })

  it('a hydrate backfill retires twins the stream already echoed', () => {
    const next = grillReducer(sent(), {
      type: 'hydrate',
      detail: { session: session({ state: 'waiting' }), messages: [answer('7', 'A')] },
    })
    expect(next.pending).toEqual([])
  })

  // A refetch re-hydrates the whole transcript, old turns included.
  it('a re-hydrate of an already-held answer leaves an in-flight twin alone', () => {
    const backfill = {
      type: 'hydrate' as const,
      detail: { session: session({ state: 'waiting' }), messages: [answer('7', 'A')] },
    }
    const held = grillReducer(initial, backfill)
    const again = grillReducer(grillReducer(held, { type: 'send', id: 'p1', text: 'A' }), backfill)
    expect(again.pending.map((p) => p.id)).toEqual(['p1'])
  })

  it('a failed send keeps its text for retry and is not retired by an echo', () => {
    const failed = grillReducer(sent(), { type: 'send-failed', id: 'p1', text: 'A' })
    expect(failed.pending[0].failed).toBe(true)
    const echoed = grillReducer(failed, { type: 'message', message: answer('7', 'A') })
    expect(echoed.pending.map((p) => p.id)).toEqual(['p1'])
  })

  it('surfaces the failure on a surviving twin when the echo retired its own entry', () => {
    const twice = grillReducer(sent(), { type: 'send', id: 'p2', text: 'A' })
    const echoed = grillReducer(twice, { type: 'message', message: answer('7', 'A') })
    const failed = grillReducer(echoed, { type: 'send-failed', id: 'p1', text: 'A' })
    expect(failed.pending).toEqual([{ id: 'p2', text: 'A', failed: true }])
  })

  it('retry clears the failure so the next echo retires it', () => {
    const failed = grillReducer(sent(), { type: 'send-failed', id: 'p1', text: 'A' })
    const again = grillReducer(failed, { type: 'send-retry', id: 'p1' })
    expect(again.pending[0].failed).toBe(false)
    const echoed = grillReducer(again, { type: 'message', message: answer('7', 'A') })
    expect(echoed.pending).toEqual([])
  })

  it('discard drops the send outright', () => {
    const failed = grillReducer(sent(), { type: 'send-failed', id: 'p1', text: 'A' })
    expect(grillReducer(failed, { type: 'send-discard', id: 'p1' }).pending).toEqual([])
  })
})

describe('composer gating', () => {
  it('takes typing only in the states that can accept an answer', () => {
    expect(canCompose('waiting')).toBe(true)
    expect(canCompose('parked')).toBe(true)
    expect(canCompose('running')).toBe(false)
    expect(canCompose('finished')).toBe(false)
  })

  it('shuts the box on a stalled session — its Resume button carries the answer', () => {
    expect(isAwaitingAnswer('stalled')).toBe(true)
    expect(canCompose('stalled')).toBe(false)
    expect(composerPlaceholder('stalled')).toBe('Session stalled — resume to keep answering…')
  })

  it('explains a disabled box while the agent is thinking', () => {
    expect(composerPlaceholder('running')).toBe('Agent is thinking…')
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
    expect(compact(lines)).toEqual([
      'equal a',
      'delete b',
      'insert B',
      'equal c',
    ])
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

function compact(lines: DiffLine[]): string[] {
  return lines.map((l) => `${l.op} ${l.text}`)
}

function answerFor(list: GrillMessage[], id: string): string {
  const found = list.find((m) => m.id === id)
  const payload = (found?.payload ?? {}) as { text?: string }
  return payload.text ?? ''
}
