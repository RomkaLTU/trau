import { describe, expect, it } from 'vitest'

import type { BacklogEntry } from './backlog'
import type { GrillSession, GrillState, PregrillResponse } from './grill'
import {
  buildInbox,
  compareIssueIds,
  doneTodayItems,
  inboxAttention,
  inboxCounts,
  inboxGroups,
  inboxPill,
  inboxPosition,
  isToday,
  mergeGrillableEntries,
  nextIssueId,
  prevIssueId,
  skipTarget,
  summarisePregrill,
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

function session(over: Partial<GrillSession> & { state: GrillState }): GrillSession {
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
    expect(items.map((i) => i.id)).toEqual(['COD-3', 'COD-4', 'COD-1', 'COD-5', 'COD-2'])
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

describe('isToday', () => {
  const now = new Date('2026-07-15T09:00:00')

  it('compares on the local calendar day, not on elapsed time', () => {
    expect(isToday('2026-07-15T23:30:00', now)).toBe(true)
    expect(isToday('2026-07-14T23:30:00', now)).toBe(false)
    expect(isToday('2027-07-15T09:00:00', now)).toBe(false)
  })

  it('rejects an unparseable timestamp', () => {
    expect(isToday('', now)).toBe(false)
  })
})

describe('doneTodayItems', () => {
  const now = new Date('2026-07-15T09:00:00')
  const today = new Date('2026-07-15T08:00:00').toISOString()
  const yesterday = new Date('2026-07-14T08:00:00').toISOString()

  it('takes today’s applied sessions and titles them from the session', () => {
    const items = doneTodayItems(
      [
        session({
          id: '3',
          issue_id: 'COD-1',
          issue_title: 'Split the picker',
          state: 'applied',
          updated_at: today,
        }),
        session({ id: '2', issue_id: 'COD-2', state: 'applied', updated_at: yesterday }),
        session({ id: '1', issue_id: 'COD-3', state: 'finished', updated_at: today }),
      ],
      now,
    )
    expect(items.map((i) => i.id)).toEqual(['COD-1'])
    expect(items[0]).toMatchObject({ title: 'Split the picker', attention: 'done' })
    expect(items[0].entry).toBeUndefined()
  })

  it('keeps one row per issue and drops an applied authoring session', () => {
    const items = doneTodayItems(
      [
        session({ id: '3', issue_id: 'COD-1', state: 'applied', updated_at: today }),
        session({ id: '2', issue_id: 'COD-1', state: 'applied', updated_at: today }),
        session({ id: '1', state: 'applied', updated_at: today }),
      ],
      now,
    )
    expect(items.map((i) => i.session?.id)).toEqual(['3'])
  })
})

describe('inboxGroups', () => {
  it('splits the queue into the three rail groups, keeping empty ones', () => {
    const items = buildInbox(
      [entry({ id: 'COD-1' }), entry({ id: 'COD-2' }), entry({ id: 'COD-3' })],
      [
        session({ id: '1', issue_id: 'COD-1', state: 'waiting' }),
        session({ id: '2', issue_id: 'COD-2', state: 'running' }),
      ],
    )
    const groups = inboxGroups(items)
    expect(groups.map((g) => g.group)).toEqual(['waiting', 'review', 'done'])
    expect(groups[0].label).toBe('Waiting for you')
    // A question, a running turn and an untouched issue are all still owed a turn.
    expect(groups[0].items.map((i) => i.id)).toEqual(['COD-1', 'COD-2', 'COD-3'])
    expect(groups[1].items).toEqual([])
    expect(groups[2].items).toEqual([])
  })

  it('files finished sessions under review and applied ones under done', () => {
    const items = buildInbox(
      [entry({ id: 'COD-1' })],
      [session({ id: '1', issue_id: 'COD-1', state: 'finished' })],
    )
    const done = doneTodayItems(
      [
        session({
          id: '2',
          issue_id: 'COD-9',
          state: 'applied',
          updated_at: new Date('2026-07-14T10:00:00').toISOString(),
        }),
      ],
      new Date('2026-07-14T12:00:00'),
    )
    const groups = inboxGroups(items, done)
    expect(groups[0].items).toEqual([])
    expect(groups[1].items.map((i) => i.id)).toEqual(['COD-1'])
    expect(groups[2].items.map((i) => i.id)).toEqual(['COD-9'])
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

  // j/k step the queue, but the rail is what the user is reading: the attention tiers
  // buildInbox sorts by have to land in the rail's group order, or the selection jumps
  // around the sections it walks.
  it('steps the queue in the order the rail lays its groups out', () => {
    const queue = buildInbox(
      [
        entry({ id: 'COD-1' }),
        entry({ id: 'COD-2' }),
        entry({ id: 'COD-3' }),
        entry({ id: 'COD-4' }),
      ],
      [
        session({ id: '1', issue_id: 'COD-1', state: 'finished' }),
        session({ id: '2', issue_id: 'COD-2', state: 'waiting' }),
        session({ id: '3', issue_id: 'COD-3', state: 'running' }),
      ],
    )
    const rail = inboxGroups(queue)
      .filter((g) => g.group !== 'done')
      .flatMap((g) => g.items.map((i) => i.id))
    expect(rail).toEqual(queue.map((i) => i.id))
    expect(rail).toEqual(['COD-2', 'COD-3', 'COD-4', 'COD-1'])
  })
})

describe('skipTarget', () => {
  const items = buildInbox(
    [entry({ id: 'COD-1' }), entry({ id: 'COD-2' }), entry({ id: 'COD-3' })],
    [],
  )

  it('advances, and wraps past the last item so a skipped one comes round again', () => {
    expect(skipTarget(items, 'COD-1')).toBe('COD-2')
    expect(skipTarget(items, 'COD-3')).toBe('COD-1')
  })

  it('restarts at the head when the id has left the queue or none is selected', () => {
    expect(skipTarget(items, 'COD-9')).toBe('COD-1')
    expect(skipTarget(items, null)).toBe('COD-1')
  })

  it('has nowhere to go in an empty queue', () => {
    expect(skipTarget([], 'COD-1')).toBeNull()
  })
})

describe('inboxPill', () => {
  it('reads a session from the triager’s seat', () => {
    expect(inboxPill('waiting')).toEqual({ tone: 'warn', label: 'your turn' })
    expect(inboxPill('parked')).toEqual({ tone: 'warn', label: 'your turn' })
    expect(inboxPill('running')).toEqual({ tone: 'active', label: 'thinking' })
    expect(inboxPill('stalled')).toEqual({ tone: 'warn', label: 'stalled' })
    expect(inboxPill('finished')).toEqual({ tone: 'verify', label: 'review' })
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

describe('summarisePregrill', () => {
  function response(outcomes: PregrillResponse['results'][number]['outcome'][]): PregrillResponse {
    return {
      repo: 'acme',
      max: 5,
      results: outcomes.map((outcome, i) => ({ issue_id: `COD-${i}`, outcome })),
    }
  }

  it('names only the outcomes that occurred, pluralising', () => {
    expect(
      summarisePregrill(response(['question_parked', 'question_parked', 'rewrite_drafted', 'clear'])),
    ).toBe('Pre-grill pass: 2 questions parked · 1 rewrite drafted · 1 already clear')
  })

  it('reports errors and skips', () => {
    expect(summarisePregrill(response(['error', 'skipped']))).toBe(
      'Pre-grill pass: 1 error · 1 skipped',
    )
  })

  it('handles an empty pass', () => {
    expect(summarisePregrill(response([]))).toBe('Pre-grill pass: nothing to do.')
  })
})
