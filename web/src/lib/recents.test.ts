import { describe, expect, it } from 'vitest'

import {
  RECENTS_CAP,
  parseRecents,
  projectRecent,
  recordRecent,
  visibleRecents,
  visitRecent,
  type RecentEntry,
} from '@/lib/recents'

function page(path: string, label: string, at = 0): RecentEntry {
  return { kind: 'page', key: `page:${path}`, label, path, at }
}

function run(repo: string, ticket: string, at = 0): RecentEntry {
  return {
    kind: 'run',
    key: `run:${repo}/${ticket}`,
    label: ticket,
    sublabel: repo,
    path: `/runs/${repo}/${ticket}`,
    at,
  }
}

describe('recordRecent', () => {
  it('prepends the newest entry', () => {
    const list = recordRecent([page('/backlog', 'Backlog', 1)], page('/loop', 'Loop', 2))
    expect(list.map((e) => e.key)).toEqual(['page:/loop', 'page:/backlog'])
  })

  it('dedupes by key, moving the entry to the front', () => {
    const list = recordRecent(
      [page('/backlog', 'Backlog', 1), page('/loop', 'Loop', 2)],
      page('/backlog', 'Backlog', 3),
    )
    expect(list.map((e) => e.key)).toEqual(['page:/backlog', 'page:/loop'])
    expect(list[0].at).toBe(3)
  })

  it('caps the list, dropping the oldest', () => {
    let list: RecentEntry[] = []
    for (let i = 0; i <= RECENTS_CAP + 4; i++) {
      list = recordRecent(list, run('melga', `COD-${i}`, i))
    }
    expect(list).toHaveLength(RECENTS_CAP)
    expect(list[0].label).toBe(`COD-${RECENTS_CAP + 4}`)
    expect(list[list.length - 1].label).toBe('COD-5')
  })
})

describe('visitRecent', () => {
  it('maps a nav destination to a page entry', () => {
    expect(visitRecent('/backlog', 7)).toEqual({
      kind: 'page',
      key: 'page:/backlog',
      label: 'Backlog',
      path: '/backlog',
      at: 7,
    })
  })

  it('maps the root pathname to Overview', () => {
    expect(visitRecent('/', 1)).toMatchObject({ kind: 'page', label: 'Overview' })
  })

  it('maps a run detail to a run entry', () => {
    expect(visitRecent('/runs/melga/COD-123', 9)).toEqual({
      kind: 'run',
      key: 'run:melga/COD-123',
      label: 'COD-123',
      sublabel: 'melga',
      path: '/runs/melga/COD-123',
      at: 9,
    })
  })

  it('maps a live view to a run entry', () => {
    expect(visitRecent('/live/melga/COD-9', 1)).toMatchObject({
      kind: 'run',
      key: 'run:melga/COD-9',
    })
  })

  it('decodes url-encoded segments', () => {
    expect(visitRecent('/runs/my%20repo/COD-1', 1)).toMatchObject({
      sublabel: 'my repo',
    })
  })

  it('ignores unknown routes', () => {
    expect(visitRecent('/nope', 1)).toBeNull()
    expect(visitRecent('/runs/melga', 1)).toBeNull()
    expect(visitRecent('/runs/melga/COD-1/extra', 1)).toBeNull()
  })
})

describe('visibleRecents', () => {
  const current = { path: '/loop', repo: 'melga', repos: ['melga', 'salonradar'] }

  it('keeps newest-first order and caps at six', () => {
    const list = Array.from({ length: 8 }, (_, i) => run('melga', `COD-${8 - i}`, 8 - i))
    const visible = visibleRecents(list, current)
    expect(visible).toHaveLength(6)
    expect(visible[0].label).toBe('COD-8')
  })

  it('drops the entry for the current page', () => {
    const list = [page('/loop', 'Loop'), page('/backlog', 'Backlog')]
    expect(visibleRecents(list, current).map((e) => e.key)).toEqual([
      'page:/backlog',
    ])
  })

  it('drops the project entry for the active scope', () => {
    const list = [projectRecent('melga', 1), projectRecent('salonradar', 2)]
    expect(visibleRecents(list, current).map((e) => e.label)).toEqual([
      'salonradar',
    ])
  })

  it('drops entries whose repo is no longer registered', () => {
    const list = [projectRecent('gone', 1), run('gone', 'COD-1'), page('/costs', 'Costs')]
    expect(visibleRecents(list, current).map((e) => e.key)).toEqual([
      'page:/costs',
    ])
  })
})

describe('parseRecents', () => {
  it('returns empty for absent or corrupt storage', () => {
    expect(parseRecents(null)).toEqual([])
    expect(parseRecents('not json')).toEqual([])
    expect(parseRecents('{"kind":"page"}')).toEqual([])
  })

  it('filters malformed entries, keeping valid ones', () => {
    const valid = page('/backlog', 'Backlog', 3)
    const raw = JSON.stringify([
      valid,
      { kind: 'page', key: 'page:/x' },
      { kind: 'bogus', key: 'k', label: 'l', at: 1 },
      null,
    ])
    expect(parseRecents(raw)).toEqual([valid])
  })
})
