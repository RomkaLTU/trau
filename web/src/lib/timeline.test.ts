import { describe, expect, it } from 'vitest'

import type { Instance } from './instances'
import type { QueueItem } from './queue'
import type { Run } from './runs'
import {
  buildTimeline,
  finishedReducer,
  finishedView,
  ticketPill,
  FINISHED_INITIAL,
  FINISHED_PAGE_SIZE,
  type TimelineTicket,
} from './timeline'

function item(over: Partial<QueueItem>): QueueItem {
  return {
    position: 1,
    kind: 'ticket',
    id: 'COD-1',
    status: 'pending',
    ...over,
  }
}

function run(over: Partial<Run>): Run {
  return {
    ticket: 'COD-1',
    phase: 'building',
    phase_rank: 1,
    terminal: false,
    ...over,
  }
}

function instance(over: Partial<Instance>): Instance {
  return {
    pid: 1,
    repo: 'loop',
    repo_root: '/loop',
    runs_dir: 'runs',
    started_at: '2026-07-13T10:00:00Z',
    session_state: 'working',
    ...over,
  }
}

function ticket(t: TimelineTicket): TimelineTicket {
  return t
}

describe('buildTimeline', () => {
  it('flattens standalone tickets and epic sub-issues into leaf counts', () => {
    const tl = buildTimeline(
      [
        item({ id: 'COD-1', kind: 'ticket' }),
        item({
          id: 'COD-9',
          kind: 'epic',
          sub_issues: [
            { id: 'COD-10', title: 'a', state: 'todo' },
            { id: 'COD-11', title: 'b', state: 'todo' },
          ],
        }),
      ],
      [],
    )
    expect(tl.total).toBe(3)
    expect(tl.done).toBe(0)
  })

  it('lets the live run record win over the snapshot state', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1', status: 'pending' })],
      [run({ ticket: 'COD-1', terminal: true, phase: 'merged' })],
    )
    expect(tl.settled.map((t) => [t.id, t.status])).toEqual([['COD-1', 'done']])
    expect(tl.done).toBe(1)
    expect(tl.pending).toEqual([])
  })

  it('classifies a paused run with its failure class and reason', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1' })],
      [
        run({
          ticket: 'COD-1',
          terminal: true,
          failure_class: 'paused',
          failure_reason: 'rate limit',
        }),
      ],
    )
    const t = tl.settled[0]
    expect(t.status).toBe('paused')
    expect(t.failureClass).toBe('paused')
    expect(t.reason).toBe('rate limit')
  })

  it('classifies faulted and gave_up runs as failed', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1' }), item({ id: 'COD-2' })],
      [
        run({ ticket: 'COD-1', terminal: true, failure_class: 'faulted' }),
        run({ ticket: 'COD-2', terminal: true, failure_class: 'gave_up' }),
      ],
    )
    expect(tl.settled.map((t) => t.status)).toEqual(['failed', 'failed'])
    expect(tl.settled.map((t) => t.failureClass)).toEqual([
      'faulted',
      'gave_up',
    ])
  })

  it('orders settled tickets by actual completion time', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1' }), item({ id: 'COD-2' }), item({ id: 'COD-3' })],
      [
        run({
          ticket: 'COD-1',
          terminal: true,
          updated_at: '2026-07-13T10:05:00Z',
        }),
        run({
          ticket: 'COD-2',
          terminal: true,
          updated_at: '2026-07-13T10:02:00Z',
        }),
        run({
          ticket: 'COD-3',
          terminal: true,
          updated_at: '2026-07-13T10:09:00Z',
        }),
      ],
    )
    expect(tl.settled.map((t) => t.id)).toEqual(['COD-2', 'COD-1', 'COD-3'])
  })

  it('sorts snapshot-done tickets (no run) ahead of timestamped completions', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1', status: 'done' }), item({ id: 'COD-2' })],
      [
        run({
          ticket: 'COD-2',
          terminal: true,
          updated_at: '2026-07-13T10:02:00Z',
        }),
      ],
    )
    expect(tl.settled.map((t) => t.id)).toEqual(['COD-1', 'COD-2'])
  })

  it('marks the live instance ticket running even without a run record', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1' }), item({ id: 'COD-2' })],
      [],
      instance({ ticket: 'COD-2', phase: 'verified' }),
    )
    expect(tl.running?.id).toBe('COD-2')
    expect(tl.running?.phase).toBe('verified')
    expect(
      tl.pending.map((p) => (p.kind === 'ticket' ? p.ticket.id : p.id)),
    ).toEqual(['COD-1'])
  })

  it('leaves nothing remaining once the last queued ticket is the running one', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1', status: 'done' }), item({ id: 'COD-2' })],
      [run({ ticket: 'COD-1', terminal: true, phase: 'merged' })],
      instance({ ticket: 'COD-2' }),
    )
    expect(tl.running?.id).toBe('COD-2')
    expect(tl.pending).toEqual([])
  })

  it('carries the working instance Activity onto the running ticket', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1' })],
      [],
      instance({
        ticket: 'COD-1',
        phase: 'handed_off',
        activity: 'repair',
        detail: 'repair2',
      }),
    )
    expect(tl.running?.activity).toBe('repair')
    expect(tl.running?.detail).toBe('repair2')
  })

  it('groups an epic pending children under a header with live progress', () => {
    const tl = buildTimeline(
      [
        item({
          id: 'COD-9',
          kind: 'epic',
          title: 'Epic',
          sub_issues: [
            { id: 'COD-10', title: 'a', state: 'todo' },
            { id: 'COD-11', title: 'b', state: 'todo' },
            { id: 'COD-12', title: 'c', state: 'todo' },
          ],
        }),
      ],
      [run({ ticket: 'COD-10', terminal: true, phase: 'merged' })],
    )
    expect(tl.done).toBe(1)
    expect(tl.pending).toHaveLength(1)
    const entry = tl.pending[0]
    expect(entry.kind).toBe('epic')
    if (entry.kind === 'epic') {
      expect(entry.done).toBe(1)
      expect(entry.total).toBe(3)
      expect(entry.children.map((c) => c.id)).toEqual(['COD-11', 'COD-12'])
    }
  })

  it('carries an item source onto its ticket, and an epic source onto its children', () => {
    const tl = buildTimeline(
      [
        item({ id: 'LOOP-1', source: 'internal' }),
        item({ id: 'COD-1', source: 'linear' }),
        item({
          id: 'LOOP-2',
          kind: 'epic',
          source: 'internal',
          sub_issues: [{ id: 'LOOP-3', title: 'a', state: 'todo' }],
        }),
      ],
      [],
    )
    const sources = tl.pending.map((p) =>
      p.kind === 'epic'
        ? [p.id, p.source, p.children.map((c) => c.source)]
        : [p.ticket.id, p.ticket.source],
    )
    expect(sources).toEqual([
      ['LOOP-1', 'internal'],
      ['COD-1', 'linear'],
      ['LOOP-2', 'internal', ['internal']],
    ])
  })

  it('does not count epic group headers, only leaf tickets', () => {
    const tl = buildTimeline(
      [
        item({ id: 'COD-1', kind: 'ticket' }),
        item({
          id: 'COD-9',
          kind: 'epic',
          sub_issues: [{ id: 'COD-10', title: 'a', state: 'done' }],
        }),
      ],
      [],
    )
    expect(tl.total).toBe(2)
    expect(tl.done).toBe(1)
  })

  it('anchors elapsed to the earliest run update before the instance start', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1' }), item({ id: 'COD-2' })],
      [
        run({
          ticket: 'COD-1',
          terminal: true,
          updated_at: '2026-07-13T09:50:00Z',
        }),
      ],
      instance({ ticket: 'COD-2', started_at: '2026-07-13T10:00:00Z' }),
    )
    expect(tl.elapsedAnchor).toBe('2026-07-13T09:50:00Z')
  })

  it('keeps every ticket visible when two runs are simultaneously non-terminal', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1' }), item({ id: 'COD-2' }), item({ id: 'COD-3' })],
      [
        run({ ticket: 'COD-1', terminal: false }),
        run({ ticket: 'COD-2', terminal: false }),
      ],
    )
    const pendingIds = tl.pending.map((p) =>
      p.kind === 'ticket' ? p.ticket.id : p.id,
    )
    const seen = new Set([
      ...tl.settled.map((t) => t.id),
      ...(tl.running ? [tl.running.id] : []),
      ...pendingIds,
    ])
    expect(seen).toEqual(new Set(['COD-1', 'COD-2', 'COD-3']))
    expect(tl.running?.id).toBe('COD-1')
    expect(pendingIds).toContain('COD-2')
  })

  it('features the live instance ticket as running and keeps a stale running snapshot in remaining', () => {
    const tl = buildTimeline(
      [item({ id: 'COD-1', status: 'running' }), item({ id: 'COD-2' })],
      [],
      instance({ ticket: 'COD-2', phase: 'building' }),
    )
    expect(tl.running?.id).toBe('COD-2')
    const pendingIds = tl.pending.map((p) =>
      p.kind === 'ticket' ? p.ticket.id : p.id,
    )
    expect(pendingIds).toContain('COD-1')
  })

  it('keeps an epic sub-issue with a stale non-terminal run inside its remaining group', () => {
    const tl = buildTimeline(
      [
        item({ id: 'COD-2' }),
        item({
          id: 'COD-9',
          kind: 'epic',
          title: 'Epic',
          sub_issues: [
            { id: 'COD-10', title: 'a', state: 'todo' },
            { id: 'COD-11', title: 'b', state: 'todo' },
          ],
        }),
      ],
      [
        run({ ticket: 'COD-2', terminal: false }),
        run({ ticket: 'COD-10', terminal: false }),
      ],
    )
    expect(tl.running?.id).toBe('COD-2')
    const epic = tl.pending.find((p) => p.kind === 'epic')
    expect(epic?.kind).toBe('epic')
    if (epic?.kind === 'epic') {
      expect(epic.children.map((c) => c.id)).toEqual(['COD-10', 'COD-11'])
    }
  })
})

describe('finishedView', () => {
  function settledTicket(
    id: string,
    over: Partial<TimelineTicket> = {},
  ): TimelineTicket {
    return ticket({ id, title: '', status: 'done', hasRun: true, ...over })
  }

  it('tallies every settle status and drops the ones nothing settled into', () => {
    const view = finishedView(
      [
        settledTicket('COD-1'),
        settledTicket('COD-2'),
        settledTicket('COD-3', { hasRun: false }),
        settledTicket('COD-4', { status: 'failed' }),
        settledTicket('COD-5', { status: 'paused' }),
      ],
      FINISHED_PAGE_SIZE,
    )
    expect(view.total).toBe(5)
    expect(view.tally).toEqual([
      { label: 'merged', count: 2 },
      { label: 'done', count: 1 },
      { label: 'failed', count: 1 },
      { label: 'paused', count: 1 },
    ])
  })

  it('reads newest-first and features the newest completion as latest', () => {
    const view = finishedView(
      [settledTicket('COD-1'), settledTicket('COD-2'), settledTicket('COD-3')],
      FINISHED_PAGE_SIZE,
    )
    expect(view.rows.map((t) => t.id)).toEqual(['COD-3', 'COD-2', 'COD-1'])
    expect(view.latest?.id).toBe('COD-3')
    expect(view.older).toBe(0)
  })

  it('caps rows at the visible count and reports the older remainder', () => {
    const settled = Array.from({ length: 12 }, (_, i) =>
      settledTicket(`COD-${i + 1}`),
    )

    const firstPage = finishedView(settled, FINISHED_PAGE_SIZE)
    expect(firstPage.rows.map((t) => t.id)).toEqual([
      'COD-12',
      'COD-11',
      'COD-10',
      'COD-9',
      'COD-8',
      'COD-7',
      'COD-6',
      'COD-5',
      'COD-4',
      'COD-3',
    ])
    expect(firstPage.older).toBe(2)

    const secondPage = finishedView(settled, FINISHED_PAGE_SIZE * 2)
    expect(secondPage.rows).toHaveLength(12)
    expect(secondPage.older).toBe(0)
  })
})

describe('finishedReducer', () => {
  it('resets pagination when the section collapses', () => {
    const expanded = finishedReducer(FINISHED_INITIAL, { type: 'toggle' })
    expect(expanded.expanded).toBe(true)

    const paged = finishedReducer(finishedReducer(expanded, { type: 'more' }), {
      type: 'more',
    })
    expect(paged.visible).toBe(FINISHED_PAGE_SIZE * 3)

    expect(finishedReducer(paged, { type: 'toggle' })).toEqual(FINISHED_INITIAL)
  })
})

describe('ticketPill', () => {
  it('maps each status to the matching pill state', () => {
    expect(
      ticketPill(ticket({ id: 'a', title: '', status: 'done', hasRun: true })),
    ).toEqual({ state: 'success', label: 'merged' })
    expect(
      ticketPill(ticket({ id: 'a', title: '', status: 'done', hasRun: false })),
    ).toEqual({ state: 'success', label: 'done' })
    expect(
      ticketPill(
        ticket({
          id: 'a',
          title: '',
          status: 'running',
          phase: 'verified',
          hasRun: true,
        }),
      ),
    ).toEqual({ state: 'active', label: 'ship' })
    expect(
      ticketPill(
        ticket({
          id: 'a',
          title: '',
          status: 'running',
          phase: 'handed_off',
          activity: 'repair',
          hasRun: true,
        }),
      ),
    ).toEqual({ state: 'active', label: 'verify' })
    expect(
      ticketPill(
        ticket({ id: 'a', title: '', status: 'paused', hasRun: true }),
      ),
    ).toEqual({ state: 'warn', label: 'paused' })
    expect(
      ticketPill(
        ticket({
          id: 'a',
          title: '',
          status: 'failed',
          failureClass: 'gave_up',
          hasRun: true,
        }),
      ),
    ).toEqual({ state: 'fail', label: 'quarantined' })
    expect(
      ticketPill(
        ticket({
          id: 'a',
          title: '',
          status: 'failed',
          failureClass: 'faulted',
          hasRun: true,
        }),
      ),
    ).toEqual({ state: 'fail', label: 'fault' })
    expect(
      ticketPill(
        ticket({ id: 'a', title: '', status: 'skipped', hasRun: false }),
      ),
    ).toEqual({ state: 'info', label: 'skipped' })
    expect(
      ticketPill(
        ticket({ id: 'a', title: '', status: 'pending', hasRun: false }),
      ),
    ).toEqual({ state: 'todo', label: 'pending' })
  })
})
