import { describe, expect, it } from 'vitest'

import type { Instance } from '@/lib/instances'
import { RUN_SUGGESTION_CAP, suggestFor } from '@/lib/suggestions'

function inst(
  repo: string,
  ticket: string | undefined,
  state = 'working',
): Instance {
  return {
    pid: 1,
    repo,
    repo_root: `/tmp/${repo}`,
    runs_dir: 'runs',
    started_at: '2026-07-18T00:00:00Z',
    session_state: state,
    ticket,
  }
}

function args(overrides: Partial<Parameters<typeof suggestFor>[0]> = {}) {
  return { pathname: '/settings', scope: 'melga', instances: [], ...overrides }
}

function labelsOf(entries: ReturnType<typeof suggestFor>) {
  return entries.map((e) => (e.kind === 'page' ? e.item.label : e.key))
}

describe('suggestFor active runs', () => {
  it('suggests a live jump for an active run with a ticket', () => {
    const entries = suggestFor(args({ instances: [inst('melga', 'COD-1')] }))
    expect(entries).toEqual([
      {
        kind: 'live',
        key: 'live:melga/COD-1',
        label: 'melga · COD-1',
        path: '/live/melga/COD-1',
      },
    ])
  })

  it('includes grazing and stopping, excludes parked, idle, and ticketless', () => {
    const entries = suggestFor(
      args({
        instances: [
          inst('melga', 'COD-1', 'grazing'),
          inst('melga', 'COD-2', 'stopping'),
          inst('melga', 'COD-3', 'parked'),
          inst('melga', 'COD-4', 'idle'),
          inst('melga', undefined, 'working'),
        ],
      }),
    )
    expect(entries.map((e) => e.key)).toEqual([
      'live:melga/COD-1',
      'live:melga/COD-2',
    ])
  })

  it('lists runs for the active scope before the rest', () => {
    const entries = suggestFor(
      args({
        scope: 'salonradar',
        instances: [inst('melga', 'COD-1'), inst('salonradar', 'COD-2')],
      }),
    )
    expect(entries.map((e) => e.key)).toEqual([
      'live:salonradar/COD-2',
      'live:melga/COD-1',
    ])
  })

  it('caps at four', () => {
    const instances = Array.from({ length: 6 }, (_, i) =>
      inst('melga', `COD-${i}`),
    )
    const entries = suggestFor(args({ instances }))
    expect(entries).toHaveLength(RUN_SUGGESTION_CAP)
  })

  it('excludes the run being viewed live', () => {
    const entries = suggestFor(
      args({
        pathname: '/live/melga/COD-1',
        instances: [inst('melga', 'COD-1'), inst('melga', 'COD-2')],
      }),
    )
    expect(entries.map((e) => e.key)).toEqual([
      'live:melga/COD-2',
      'run:melga/COD-1',
    ])
  })

  it('url-encodes repo and ticket in the path', () => {
    const entries = suggestFor(args({ instances: [inst('my repo', 'COD-1')] }))
    expect(entries[0]).toMatchObject({ path: '/live/my%20repo/COD-1' })
  })
})

describe('suggestFor route pages', () => {
  it.each([
    ['/', ['Backlog', 'Runs']],
    ['/backlog', ['Inbox', 'Loop', 'Runs']],
    ['/inbox', ['Backlog']],
    ['/loop', ['Backlog', 'Runs']],
    ['/runs', ['Costs']],
  ])('maps %s to related pages', (pathname, expected) => {
    expect(labelsOf(suggestFor(args({ pathname })))).toEqual(expected)
  })

  it('yields nothing for an unmapped route', () => {
    expect(suggestFor(args({ pathname: '/settings' }))).toEqual([])
  })

  it('lists active runs before pages', () => {
    const entries = suggestFor(
      args({ pathname: '/', instances: [inst('melga', 'COD-1')] }),
    )
    expect(labelsOf(entries)).toEqual(['live:melga/COD-1', 'Backlog', 'Runs'])
  })

  it('suggests the live view on a run detail only while active', () => {
    const running = suggestFor(
      args({
        pathname: '/runs/melga/COD-1',
        instances: [inst('melga', 'COD-1')],
      }),
    )
    expect(labelsOf(running)).toEqual(['live:melga/COD-1', 'Runs'])

    const stopped = suggestFor(args({ pathname: '/runs/melga/COD-1' }))
    expect(labelsOf(stopped)).toEqual(['Runs'])
  })

  it('suggests the run detail from a live view', () => {
    const entries = suggestFor(args({ pathname: '/live/melga/COD-1' }))
    expect(entries).toEqual([
      {
        kind: 'run',
        key: 'run:melga/COD-1',
        label: 'melga · COD-1',
        path: '/runs/melga/COD-1',
      },
    ])
  })
})

describe('suggestFor under "All repos"', () => {
  it('drops gated pages so no suggestion dead-ends', () => {
    expect(
      labelsOf(suggestFor(args({ pathname: '/', scope: null }))),
    ).toEqual(['Runs'])
    expect(suggestFor(args({ pathname: '/inbox', scope: null }))).toEqual([])
  })

  it('keeps ungated pages and live jumps', () => {
    const entries = suggestFor(
      args({
        pathname: '/runs',
        scope: null,
        instances: [inst('melga', 'COD-1')],
      }),
    )
    expect(labelsOf(entries)).toEqual(['live:melga/COD-1', 'Costs'])
  })
})
