import { describe, expect, it } from 'vitest'

import type { BacklogEntry } from './backlog'
import type { GrillSession, GrillState } from './grill'
import {
  buildInbox,
  compareIssueIds,
  inboxAttention,
  inboxCounts,
  inboxPosition,
  inboxSections,
  mergeGrillableEntries,
  nextIssueId,
  prevIssueId,
} from './inbox'

function entry(over: Partial<BacklogEntry> & { id: string }): BacklogEntry {
  return {
    title: 'title ' + over.id,
    status: 'Backlog',
    group: 'backlog',
    labels: ['needs-triage'],
    source: 'linear',
    has_children: false,
    ready: false,
    ...over,
  }
}

function session(over: Partial<GrillSession> & { issue_id: string; state: GrillState }): GrillSession {
  return {
    id: '1',
    repo: 'loop',
    created_at: '2026-07-14T10:00:00Z',
    updated_at: '2026-07-14T10:00:00Z',
    ...over,
  }
}

describe('inboxAttention', () => {
  it('maps the session state to an attention tier', () => {
    expect(inboxAttention(undefined)).toBe('open')
    expect(inboxAttention(session({ issue_id: 'COD-1', state: 'waiting' }))).toBe('answer')
    expect(inboxAttention(session({ issue_id: 'COD-1', state: 'parked' }))).toBe('answer')
    expect(inboxAttention(session({ issue_id: 'COD-1', state: 'stalled' }))).toBe('answer')
    expect(inboxAttention(session({ issue_id: 'COD-1', state: 'finished' }))).toBe('review')
    expect(inboxAttention(session({ issue_id: 'COD-1', state: 'running' }))).toBe('thinking')
  })
})

describe('mergeGrillableEntries', () => {
  it('dedupes an issue carrying two triage labels and orders by group then id', () => {
    const triage = [entry({ id: 'COD-100', group: 'backlog' }), entry({ id: 'COD-9', group: 'started' })]
    const split = [entry({ id: 'COD-100', group: 'backlog' }), entry({ id: 'COD-9', group: 'started' })]
    const merged = mergeGrillableEntries([triage, split, undefined])
    expect(merged.map((e) => e.id)).toEqual(['COD-9', 'COD-100'])
  })
})

describe('buildInbox', () => {
  it('sorts answer, then thinking, then untouched, then review, keeping id order within a tier', () => {
    const entries = [
      entry({ id: 'COD-1' }),
      entry({ id: 'COD-2' }),
      entry({ id: 'COD-3' }),
      entry({ id: 'COD-4' }),
      entry({ id: 'COD-5' }),
    ]
    const sessions = [
      session({ id: '10', issue_id: 'COD-2', state: 'finished' }),
      session({ id: '11', issue_id: 'COD-3', state: 'parked' }),
      session({ id: '12', issue_id: 'COD-4', state: 'running' }),
      session({ id: '13', issue_id: 'COD-5', state: 'applied' }),
    ]
    const items = buildInbox(entries, sessions)
    expect(items.map((i) => i.entry.id)).toEqual(['COD-3', 'COD-4', 'COD-1', 'COD-5', 'COD-2'])
    expect(items.map((i) => i.attention)).toEqual(['answer', 'thinking', 'open', 'open', 'review'])
  })

  it('treats a settled session as untouched', () => {
    const items = buildInbox([entry({ id: 'COD-1' })], [
      session({ id: '9', issue_id: 'COD-1', state: 'abandoned' }),
    ])
    expect(items[0].attention).toBe('open')
    expect(items[0].session).toBeUndefined()
  })
})

describe('inboxSections', () => {
  it('groups contiguous attention runs', () => {
    const items = buildInbox(
      [entry({ id: 'COD-1' }), entry({ id: 'COD-2' }), entry({ id: 'COD-3' })],
      [
        session({ id: '1', issue_id: 'COD-1', state: 'waiting' }),
        session({ id: '2', issue_id: 'COD-2', state: 'parked' }),
      ],
    )
    const sections = inboxSections(items)
    expect(sections.map((s) => s.attention)).toEqual(['answer', 'open'])
    expect(sections[0].items).toHaveLength(2)
    expect(sections[1].items).toHaveLength(1)
  })
})

describe('inboxCounts', () => {
  it('reports total and the awaiting-answer subset', () => {
    const items = buildInbox(
      [entry({ id: 'COD-1' }), entry({ id: 'COD-2' }), entry({ id: 'COD-3' })],
      [
        session({ id: '1', issue_id: 'COD-1', state: 'parked' }),
        session({ id: '2', issue_id: 'COD-2', state: 'finished' }),
      ],
    )
    expect(inboxCounts(items)).toEqual({ total: 3, awaiting: 1 })
  })
})

describe('walk-through navigation', () => {
  const items = buildInbox(
    [entry({ id: 'COD-1' }), entry({ id: 'COD-2' }), entry({ id: 'COD-3' })],
    [],
  )

  it('locates an issue and steps forward and back', () => {
    expect(inboxPosition(items, 'COD-2')).toBe(1)
    expect(nextIssueId(items, 'COD-2')).toBe('COD-3')
    expect(prevIssueId(items, 'COD-2')).toBe('COD-1')
  })

  it('returns null at the ends and for a missing id', () => {
    expect(nextIssueId(items, 'COD-3')).toBeNull()
    expect(prevIssueId(items, 'COD-1')).toBeNull()
    expect(nextIssueId(items, 'COD-9')).toBeNull()
    expect(inboxPosition(items, 'COD-9')).toBe(-1)
  })
})

describe('compareIssueIds', () => {
  it('orders numerically within a prefix', () => {
    expect(['COD-100', 'COD-9', 'COD-20'].sort(compareIssueIds)).toEqual([
      'COD-9',
      'COD-20',
      'COD-100',
    ])
  })

  it('falls back to a string compare across prefixes or non-numeric suffixes', () => {
    expect(compareIssueIds('AAA-1', 'COD-1')).toBeLessThan(0)
    expect(compareIssueIds('feature', 'feature')).toBe(0)
  })
})
