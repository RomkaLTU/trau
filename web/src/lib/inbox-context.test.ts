import { describe, expect, it } from 'vitest'

import type { GrillMessage, GrillKind } from './grill'
import { contextRows } from './inbox-context'

const NOW = new Date('2026-07-15T12:00:00Z')

function message(id: number, kind: GrillKind): GrillMessage {
  return {
    id: String(id),
    role: kind === 'answer' ? 'user' : 'agent',
    kind,
    payload: {},
    created_at: '2026-07-15T10:00:00Z',
  }
}

function rows(over: Parameters<typeof contextRows>[0]) {
  return Object.fromEntries(contextRows(over).map((r) => [r.label, r.value]))
}

describe('contextRows', () => {
  it('ages the created timestamp against now', () => {
    expect(
      rows({ created: '2026-07-13T12:00:00Z', messages: [], now: NOW }).created,
    ).toBe('2d ago')
    expect(
      rows({ created: '2026-07-15T09:00:00Z', messages: [], now: NOW }).created,
    ).toBe('3h ago')
    expect(
      rows({ created: '2026-07-15T11:30:00Z', messages: [], now: NOW }).created,
    ).toBe('30m ago')
    expect(
      rows({ created: '2026-07-15T11:59:30Z', messages: [], now: NOW }).created,
    ).toBe('just now')
  })

  it('dashes a created timestamp the store never had', () => {
    expect(rows({ messages: [], now: NOW }).created).toBe('—')
    expect(rows({ created: 'not a date', messages: [], now: NOW }).created).toBe('—')
  })

  it('reads anything the tracker synced as tracker, and only internal as internal', () => {
    expect(rows({ source: 'internal', messages: [], now: NOW }).source).toBe('internal')
    expect(rows({ source: 'linear', messages: [], now: NOW }).source).toBe('tracker')
    expect(rows({ source: 'jira', messages: [], now: NOW }).source).toBe('tracker')
    expect(rows({ messages: [], now: NOW }).source).toBe('tracker')
  })

  it('counts a pending question as the one still outstanding', () => {
    const messages = [
      message(1, 'question'),
      message(2, 'answer'),
      message(3, 'question'),
    ]
    expect(rows({ messages, now: NOW }).progress).toBe('1 of 2 questions answered')
  })

  it('counts every question answered once the last one has a reply', () => {
    const messages = [
      message(1, 'question'),
      message(2, 'answer'),
      message(3, 'question'),
      message(4, 'answer'),
    ]
    expect(rows({ messages, now: NOW }).progress).toBe('2 of 2 questions answered')
  })

  it('ignores info and outcome turns, and singularises one question', () => {
    const messages = [
      message(1, 'info'),
      message(2, 'question'),
      message(3, 'answer'),
      message(4, 'outcome'),
    ]
    expect(rows({ messages, now: NOW }).progress).toBe('1 of 1 question answered')
  })

  it('reports an untouched session as nothing asked', () => {
    expect(rows({ messages: [], now: NOW }).progress).toBe('0 of 0 questions answered')
  })
})
