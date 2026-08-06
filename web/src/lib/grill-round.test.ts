import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  answerGrillRound,
  grillProgress,
  grillReducer,
  isRoundAnswer,
  mergeMessages,
  NO_ACTIVITY,
  NO_REPLY,
  pendingQuestion,
  roundAnswers,
  roundQuestions,
  type GrillLive,
  type GrillMessage,
  type GrillSession,
  type RoundQuestion,
} from './grill'

function session(over: Partial<GrillSession> = {}): GrillSession {
  return {
    id: '1',
    repo: 'loop',
    issue_id: 'COD-1',
    state: 'waiting',
    created_at: '2026-08-06T10:00:00Z',
    updated_at: '2026-08-06T10:00:00Z',
    ...over,
  }
}

function msg(over: Partial<GrillMessage>): GrillMessage {
  return {
    id: '1',
    role: 'agent',
    kind: 'info',
    payload: {},
    created_at: '2026-08-06T10:00:00Z',
    ...over,
  }
}

// round builds the message shape the hub stores for ask_round: the numbered text every
// single-question reader falls back to, and the questions themselves.
function round(over: Partial<GrillMessage> = {}): GrillMessage {
  return msg({
    id: '10',
    kind: 'question',
    payload: {
      text: '1. Which page?\n2. Which auth flow?',
      round: [
        { text: 'Which page?', options: ['login', 'signup'], recommended: 'login' },
        { text: 'Which auth flow?', allow_free_text: false, options: ['password'] },
      ],
    },
    ...over,
  })
}

function live(over: Partial<GrillLive> = {}): GrillLive {
  return {
    session: session(),
    live: false,
    hydrated: true,
    messages: [],
    pending: [],
    streaming: NO_REPLY,
    activity: NO_ACTIVITY,
    ...over,
  }
}

describe('roundQuestions', () => {
  it('reads a round with each question on questionPayload’s own defaults', () => {
    const got = roundQuestions(round()) as RoundQuestion[]
    expect(got).toHaveLength(2)
    expect(got[0]).toEqual({
      text: 'Which page?',
      options: ['login', 'signup'],
      recommended: 'login',
      why: undefined,
      allow_free_text: true,
    })
    expect(got[1].allow_free_text).toBe(false)
  })

  it('leaves a single question to ask_user’s own reader', () => {
    expect(roundQuestions(msg({ kind: 'question', payload: { text: 'Which page?' } }))).toBeNull()
    expect(roundQuestions(msg({ kind: 'question', payload: { text: 'x', round: [] } }))).toBeNull()
  })
})

describe('roundAnswers', () => {
  it('orders the answers by the question each one settles and drops malformed ones', () => {
    const got = roundAnswers(
      round({
        round_answers: [
          { index: 1, text: 'password' },
          { index: 0, text: 'login', auto: true },
          { index: 2, text: 3 } as never,
        ],
      }),
    )
    expect(got).toEqual([
      { index: 0, text: 'login', auto: true },
      { index: 1, text: 'password', auto: false },
    ])
  })

  it('reads no answers off a round nobody has answered', () => {
    expect(roundAnswers(round())).toEqual([])
  })
})

describe('isRoundAnswer', () => {
  it('marks the answer that closed a round, so the thread leaves it out', () => {
    expect(isRoundAnswer(msg({ kind: 'answer', payload: { text: '…', round: true } }))).toBe(true)
    expect(isRoundAnswer(msg({ kind: 'answer', payload: { text: 'login' } }))).toBe(false)
  })
})

// An interjection is the user moving the conversation on: the hub never re-poses the
// round behind it, so the panel must stop treating it as the way in — otherwise the
// composer locks the moment the turn stops running.
describe('pendingQuestion', () => {
  it('holds an open round while nothing of the user’s follows it', () => {
    expect(pendingQuestion([round({ id: '10' })])?.id).toBe('10')
  })

  it('retires a round an interjection has overtaken', () => {
    const messages = [
      round({ id: '10' }),
      msg({ id: '11', kind: 'interjection', role: 'user', payload: { text: 'drop that' } }),
    ]
    expect(pendingQuestion(messages)).toBeNull()
  })
})

describe('grillProgress', () => {
  it('counts every question a round poses, answered and open alike', () => {
    const messages = [
      msg({ id: '1', kind: 'question', payload: { text: 'Which repo?' } }),
      msg({ id: '2', kind: 'answer', role: 'user', payload: { text: 'loop' } }),
      round({ id: '10', round_answers: [{ index: 0, text: 'login', auto: true }] }),
    ]
    expect(grillProgress(messages)).toEqual({ answered: 2, total: 3 })
  })

  it('counts a settled round whole', () => {
    const messages = [
      round({ id: '10' }),
      msg({ id: '11', kind: 'answer', role: 'user', payload: { text: '…', round: true } }),
    ]
    expect(grillProgress(messages)).toEqual({ answered: 2, total: 2 })
  })
})

describe('grillReducer round frames', () => {
  it('lands each answer on the round it belongs to without settling the turn', () => {
    const state = live({
      messages: [round()],
      streaming: { seq: 2, text: 'thinking…', holed: false },
    })
    const next = grillReducer(state, {
      type: 'round',
      round: { message_id: '10', answers: [{ index: 0, text: 'login', auto: true }] },
    })
    expect(roundAnswers(next.messages[0])).toEqual([{ index: 0, text: 'login', auto: true }])
    expect(next.streaming).toEqual(state.streaming)
  })

  it('ignores a frame for a round the thread does not hold', () => {
    const state = live({ messages: [round()] })
    expect(
      grillReducer(state, {
        type: 'round',
        round: { message_id: '99', answers: [{ index: 0, text: 'login' }] },
      }),
    ).toBe(state)
  })
})

// A round only ever gains answers, so the message frame that re-poses it — the SSE
// reconnect backfill, or a hydrate racing the stream — must never take answers back.
describe('mergeMessages', () => {
  it('holds a round’s answers against a copy of the message carrying fewer', () => {
    const held = [round({ round_answers: [{ index: 0, text: 'login' }] })]
    expect(roundAnswers(mergeMessages(held, [round()])[0])).toEqual([
      { index: 0, text: 'login', auto: false },
    ])
    const grown = round({
      round_answers: [
        { index: 0, text: 'login' },
        { index: 1, text: 'password' },
      ],
    })
    expect(roundAnswers(mergeMessages(held, [grown])[0])).toHaveLength(2)
  })
})

describe('answerGrillRound', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('submits the answers by the question each one settles', async () => {
    const res = { session: session({ state: 'running' }) }
    const fetchMock = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 200, json: async () => res } as Response)
    vi.stubGlobal('fetch', fetchMock)

    expect(
      await answerGrillRound('7', [
        { index: 1, text: 'password' },
        { index: 0, text: 'login', auto: true },
      ]),
    ).toEqual(res)

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/grill/7/answer')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({
      answers: [
        { index: 1, text: 'password' },
        { index: 0, text: 'login' },
      ],
    })
  })

  it('raises the hub refusal when the round is no longer the one waiting', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false,
        status: 409,
        json: async () => ({ error: 'session is not waiting on a round' }),
      } as Response),
    )
    await expect(answerGrillRound('7', [{ index: 0, text: 'login' }])).rejects.toThrow(
      'session is not waiting on a round',
    )
  })
})
