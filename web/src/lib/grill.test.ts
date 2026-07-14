import { describe, expect, it } from 'vitest'

import {
  activeSessionForIssue,
  diffHasChanges,
  diffLines,
  grillBanner,
  grillReducer,
  isAwaitingAnswer,
  isGrillable,
  isSettled,
  mergeMessages,
  outcomePayload,
  pendingQuestion,
  questionPayload,
  upsertMessage,
  type DiffLine,
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
})

describe('grillReducer', () => {
  const initial: GrillLive = { session: session({ state: 'running' }), live: false, messages: [] }

  it('hydrate seeds messages and adopts the session while not yet live', () => {
    const next = grillReducer(initial, {
      type: 'hydrate',
      detail: { session: session({ state: 'waiting' }), messages: [question('1')] },
    })
    expect(next.session.state).toBe('waiting')
    expect(next.messages.map((m) => m.id)).toEqual(['1'])
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
