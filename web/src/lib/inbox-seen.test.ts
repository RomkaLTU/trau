import { describe, expect, it } from 'vitest'

import type { GrillSession, GrillState } from './grill'
import type { InboxAttention, InboxItem } from './inbox'
import { hasUnseenQuestion, markSeen, type SeenMarks } from './inbox-seen'

const EARLIER = '2026-07-15T10:00:00Z'
const LATER = '2026-07-15T10:05:00Z'

function session(over: Partial<GrillSession> & { state: GrillState }): GrillSession {
  return {
    id: 'sess-1',
    repo: 'loop',
    created_at: EARLIER,
    updated_at: EARLIER,
    ...over,
  }
}

function item(attention: InboxAttention, over: Partial<GrillSession> = {}): InboxItem {
  return {
    id: 'COD-1',
    title: 'unclear issue',
    session: session({ state: 'waiting', ...over }),
    attention,
  }
}

describe('markSeen', () => {
  it('records a session never read before', () => {
    expect(markSeen({}, 'sess-1', EARLIER)).toEqual({ 'sess-1': EARLIER })
  })

  it('advances the mark as the session moves on', () => {
    expect(markSeen({ 'sess-1': EARLIER }, 'sess-1', LATER)).toEqual({
      'sess-1': LATER,
    })
  })

  it('keeps the marks it was given when they already read this far', () => {
    const marks: SeenMarks = { 'sess-1': LATER }
    expect(markSeen(marks, 'sess-1', LATER)).toBe(marks)
    expect(markSeen(marks, 'sess-1', EARLIER)).toBe(marks)
  })

  it('leaves other sessions alone', () => {
    expect(markSeen({ 'sess-2': EARLIER }, 'sess-1', LATER)).toEqual({
      'sess-1': LATER,
      'sess-2': EARLIER,
    })
  })
})

describe('hasUnseenQuestion', () => {
  it('lights a question on a session never opened', () => {
    expect(hasUnseenQuestion({}, item('answer'))).toBe(true)
  })

  it('clears once the session has been read', () => {
    expect(hasUnseenQuestion({ 'sess-1': EARLIER }, item('answer'))).toBe(false)
  })

  it('re-lights when the session asks again', () => {
    const asked = item('answer', { updated_at: LATER })
    expect(hasUnseenQuestion({ 'sess-1': EARLIER }, asked)).toBe(true)
  })

  it('stays clear when the read ran ahead of the rail', () => {
    expect(hasUnseenQuestion({ 'sess-1': LATER }, item('answer'))).toBe(false)
  })

  it('only speaks for a session awaiting an answer', () => {
    expect(hasUnseenQuestion({}, item('thinking'))).toBe(false)
    expect(hasUnseenQuestion({}, item('review'))).toBe(false)
    expect(hasUnseenQuestion({}, item('done'))).toBe(false)
  })

  it('says nothing about an untouched issue', () => {
    const untouched: InboxItem = { id: 'COD-2', title: 'raw', attention: 'open' }
    expect(hasUnseenQuestion({}, untouched)).toBe(false)
  })
})
