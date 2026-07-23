import { describe, expect, it } from 'vitest'

import { loopView, projectLoopState } from '@/lib/loop'
import type { Instance } from '@/lib/instances'
import type { QueueItem, QueueResponse } from '@/lib/queue'
import type { Run } from '@/lib/runs'

function instance(over: Partial<Instance>): Instance {
  return {
    pid: 1,
    repo: 'loop',
    repo_root: '/loop',
    runs_dir: 'runs',
    started_at: '2026-07-16T10:00:00Z',
    session_state: 'working',
    ...over,
  }
}

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

function queue(items: QueueItem[], draining = false): QueueResponse {
  return { repo: 'loop', draining, shutting_down: false, items }
}

const PAUSED_RUN = run({
  ticket: 'COD-1',
  failure_class: 'paused',
  failure_reason: 're-authentication needed',
  updated_at: '2026-07-23T10:00:00Z',
})

describe('loopView', () => {
  it('shows the running view while the queue drains', () => {
    expect(loopView(true)).toBe('running')
  })

  it('keeps the running view up for an active instance after the drain flag drops', () => {
    expect(loopView(false, instance({ ticket: 'COD-1' }))).toBe('running')
    expect(
      loopView(false, instance({ ticket: 'COD-1', session_state: 'stopping' })),
    ).toBe('running')
  })

  it('returns to the builder once the instance goes idle', () => {
    expect(loopView(false)).toBe('builder')
    expect(
      loopView(false, instance({ ticket: 'COD-1', session_state: 'idle' })),
    ).toBe('builder')
  })

  it('leaves a parked instance to the halt banner, not the running view', () => {
    expect(
      loopView(false, instance({ ticket: 'COD-1', session_state: 'parked' })),
    ).toBe('builder')
  })

  it('ignores an active instance that carries no ticket', () => {
    expect(loopView(false, instance({}))).toBe('builder')
  })
})

describe('projectLoopState', () => {
  it('carries the view and the queue/run join the card renders', () => {
    const state = projectLoopState({
      queue: queue([item({ id: 'COD-1' }), item({ id: 'COD-2', position: 2 })], true),
      runs: [],
    })
    expect(state.view).toBe('running')
    expect(state.timeline?.total).toBe(2)
  })

  it('has nothing to project before the queue loads', () => {
    const state = projectLoopState({ runs: [] })
    expect(state).toEqual({ view: 'builder', timeline: null, halt: null })
  })

  it('reports a current pause with its ticket and reason', () => {
    const state = projectLoopState({
      queue: queue([
        item({ id: 'COD-1', status: 'paused' }),
        item({ id: 'COD-2', position: 2 }),
      ]),
      runs: [PAUSED_RUN],
    })
    expect(state.halt).toEqual({
      kind: 'paused',
      ticket: 'COD-1',
      reason: 're-authentication needed',
    })
  })

  it('drops a historical pause once the instance is working again', () => {
    const state = projectLoopState({
      queue: queue([item({ id: 'COD-1', status: 'paused' })]),
      runs: [PAUSED_RUN],
      instance: instance({ ticket: 'COD-1', phase: 'verify' }),
    })
    expect(state.halt).toBeNull()
    expect(state.view).toBe('running')
  })

  it('drops a historical pause while another ticket runs', () => {
    const state = projectLoopState({
      queue: queue(
        [
          item({ id: 'COD-1', status: 'paused' }),
          item({ id: 'COD-2', position: 2, status: 'running' }),
        ],
        true,
      ),
      runs: [PAUSED_RUN],
      instance: instance({ ticket: 'COD-2' }),
    })
    expect(state.halt).toBeNull()
  })

  it('lets a running queue entry outrank the checkpoint it resumes from', () => {
    const state = projectLoopState({
      queue: queue([item({ id: 'COD-1', status: 'running' })], true),
      runs: [PAUSED_RUN],
    })
    expect(state.halt).toBeNull()
  })

  it('keeps a takeover clear of the halt banner', () => {
    const state = projectLoopState({
      queue: queue([item({ id: 'COD-1', status: 'paused' })]),
      runs: [PAUSED_RUN],
      instance: instance({ ticket: 'COD-1', session_state: 'takeover' }),
    })
    expect(state.halt).toBeNull()
  })

  it('still halts on a parked instance — parking is the pause', () => {
    const state = projectLoopState({
      queue: queue([item({ id: 'COD-1', status: 'paused' })]),
      runs: [PAUSED_RUN],
      instance: instance({ ticket: 'COD-1', session_state: 'parked' }),
    })
    expect(state.halt?.kind).toBe('paused')
  })

  it('halts on a parked CLI run the queue never held', () => {
    const state = projectLoopState({
      queue: queue([]),
      runs: [PAUSED_RUN],
      instance: instance({ ticket: 'COD-1', session_state: 'parked' }),
    })
    expect(state.halt).toEqual({
      kind: 'paused',
      ticket: 'COD-1',
      reason: 're-authentication needed',
    })
  })

  it('halts on a paused queue item that never recorded a run', () => {
    const state = projectLoopState({
      queue: queue([
        item({ id: 'COD-1', status: 'done' }),
        item({
          id: 'COD-2',
          position: 2,
          status: 'paused',
          reason: 'child exited without a drain report',
        }),
      ]),
      runs: [run({ ticket: 'COD-1', terminal: true, updated_at: '2026-07-23T09:00:00Z' })],
    })
    expect(state.halt).toEqual({
      kind: 'paused',
      ticket: 'COD-2',
      reason: 'child exited without a drain report',
    })
  })

  describe('epic children', () => {
    const epic = (status: string): QueueItem =>
      item({
        id: 'COD-9',
        kind: 'epic',
        status,
        sub_issues: [
          { id: 'COD-10', title: 'first', state: 'done' },
          { id: 'COD-11', title: 'second', state: 'todo' },
        ],
      })
    const runs = [
      run({ ticket: 'COD-10', terminal: true, updated_at: '2026-07-23T09:00:00Z' }),
      run({
        ticket: 'COD-11',
        failure_class: 'paused',
        failure_reason: 'usage_window',
        updated_at: '2026-07-23T10:00:00Z',
      }),
    ]

    it('names the paused child, not the epic', () => {
      const state = projectLoopState({ queue: queue([epic('paused')]), runs })
      expect(state.halt).toEqual({
        kind: 'paused',
        ticket: 'COD-11',
        reason: 'usage_window',
      })
    })

    it('clears once the resumed epic starts running again', () => {
      const state = projectLoopState({
        queue: queue([epic('running')], true),
        runs,
      })
      expect(state.halt).toBeNull()
    })
  })

  describe('failure mapping', () => {
    const haltFor = (over: Partial<Run>) =>
      projectLoopState({
        queue: queue([item({ id: 'COD-1', status: 'failed' })]),
        runs: [run({ updated_at: '2026-07-23T10:00:00Z', ...over })],
      }).halt

    it('classifies a fault', () => {
      expect(
        haltFor({ failure_class: 'faulted', failure_reason: 'dirty tree' }),
      ).toEqual({ kind: 'fault', ticket: 'COD-1', reason: 'dirty tree' })
    })

    it('classifies a non-budget give-up as quarantined', () => {
      expect(
        haltFor({ failure_class: 'gave_up', failure_reason: 'needs a human' })
          ?.kind,
      ).toBe('quarantined')
    })

    it('classifies a budget give-up as budget', () => {
      expect(
        haltFor({
          failure_class: 'gave_up',
          failure_reason: 'budget cap reached — daily ≤ $5',
        })?.kind,
      ).toBe('budget')
    })

    it('reads a clean finish as not halted', () => {
      expect(haltFor({ terminal: true })).toBeNull()
    })
  })

  it('lets the newest settle decide — an older fault does not outlive it', () => {
    const items = [
      item({ id: 'COD-1', status: 'failed' }),
      item({ id: 'COD-2', position: 2, status: 'done' }),
    ]
    const faulted = run({
      ticket: 'COD-1',
      failure_class: 'faulted',
      updated_at: '2026-07-23T09:00:00Z',
    })
    const merged = run({
      ticket: 'COD-2',
      terminal: true,
      updated_at: '2026-07-23T10:00:00Z',
    })
    expect(
      projectLoopState({ queue: queue(items), runs: [faulted, merged] }).halt,
    ).toBeNull()
    expect(
      projectLoopState({
        queue: queue(items),
        runs: [
          { ...faulted, updated_at: '2026-07-23T11:00:00Z' },
          merged,
        ],
      }).halt?.kind,
    ).toBe('fault')
  })
})
