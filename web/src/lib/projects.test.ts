import { afterEach, describe, expect, it, vi } from 'vitest'

import type { RepoView } from '@/lib/instances'
import {
  expandedOnOpen,
  filterRepoRows,
  groupRepos,
  matchesProjectDefaults,
  projectAnchor,
  projectDefaultKeys,
  projectDefaults,
  projectDefaultsModified,
  projectMembers,
  projectNameForRoot,
  removalPlan,
  removalSummary,
  renameProject,
  repoQualifiers,
  toggleExpanded,
  writeProjectTracker,
  type ProjectRemoval,
  type ProjectTracker,
  type ProjectView,
} from '@/lib/projects'

function repo(name: string, root = `/repos/${name}`): RepoView {
  return {
    name,
    root,
    runs_dir: `${root}/runs`,
    live: false,
    allowed: true,
    registered: true,
    seeded: false,
    kind: 'repo',
  }
}

function project(id: string, name: string, names: string[]): ProjectView {
  return { id, name, repos: names.map((n) => `/repos/${n}`) }
}

describe('groupRepos', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]

  it('renders a single-member project as the bare repo', () => {
    const rows = groupRepos(repos, [
      project('alpha', 'alpha', ['alpha']),
      project('bravo', 'bravo', ['bravo']),
      project('charlie', 'charlie', ['charlie']),
    ])
    expect(rows).toEqual([
      { project: null, repos: [repos[0]] },
      { project: null, repos: [repos[1]] },
      { project: null, repos: [repos[2]] },
    ])
  })

  it('renders a repo no project holds exactly as before', () => {
    expect(groupRepos(repos, [])).toEqual([
      { project: null, repos: [repos[0]] },
      { project: null, repos: [repos[1]] },
      { project: null, repos: [repos[2]] },
    ])
  })

  it('groups members under the project and keeps its member order', () => {
    const platform = project('platform', 'Platform', ['charlie', 'alpha'])
    expect(groupRepos(repos, [platform])).toEqual([
      { project: platform, repos: [repos[2], repos[0]] },
      { project: null, repos: [repos[1]] },
    ])
  })

  it('places the group where its highest-ranked member sat', () => {
    const platform = project('platform', 'Platform', ['bravo', 'charlie'])
    const rows = groupRepos(repos, [platform])
    expect(rows.map((r) => r.project?.id ?? r.repos[0].name)).toEqual([
      'alpha',
      'platform',
    ])
  })

  it('ignores members the repos list no longer carries', () => {
    const platform = project('platform', 'Platform', ['alpha', 'gone'])
    expect(groupRepos(repos, [platform])).toEqual([
      { project: null, repos: [repos[0]] },
      { project: null, repos: [repos[1]] },
      { project: null, repos: [repos[2]] },
    ])
  })
})

describe('filterRepoRows', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]
  const platform = project('platform', 'Platform', ['bravo', 'charlie'])
  const rows = groupRepos(repos, [platform])

  it('hands a blank query the same rows back', () => {
    expect(filterRepoRows(rows, '  ')).toBe(rows)
  })

  it('matches a repo name however it is cased', () => {
    expect(filterRepoRows(rows, 'ALPH')).toEqual([
      { project: null, repos: [repos[0]] },
    ])
  })

  it('matches a repo root, keeping only the members that matched', () => {
    expect(filterRepoRows(rows, '/repos/charlie')).toEqual([
      { project: platform, repos: [repos[2]] },
    ])
  })

  it('keeps every member of a group its own name matches', () => {
    expect(filterRepoRows(rows, 'platf')).toEqual([
      { project: platform, repos: [repos[1], repos[2]] },
    ])
  })

  it('drops the rows nothing matches', () => {
    expect(filterRepoRows(rows, 'nothing')).toEqual([])
  })
})

describe('projectNameForRoot', () => {
  const platform = project('platform', 'Platform', ['bravo', 'charlie'])
  const lone = project('alpha', 'Alpha Only', ['alpha'])

  it('names the project holding a root, grouped or not', () => {
    expect(projectNameForRoot('/repos/charlie', [platform, lone])).toBe(
      'Platform',
    )
    expect(projectNameForRoot('/repos/alpha', [platform, lone])).toBe(
      'Alpha Only',
    )
  })

  it('leaves a root no project holds unnamed', () => {
    expect(projectNameForRoot('/repos/delta', [platform, lone])).toBe('')
    expect(projectNameForRoot('/repos/alpha', [])).toBe('')
  })
})

describe('repoQualifiers', () => {
  it('leaves names only one repo carries alone', () => {
    expect(repoQualifiers([repo('alpha'), repo('bravo')], [])).toEqual(new Map())
  })

  it('names the project two same-named repos differ by', () => {
    const web = repo('api', '/Users/rd/web/api')
    const ops = repo('api', '/Users/rd/ops/api')
    const qualifiers = repoQualifiers(
      [web, ops],
      [
        { id: 'web', name: 'Web', repos: [web.root] },
        { id: 'ops', name: 'Ops', repos: [ops.root] },
      ],
    )

    expect(qualifiers).toEqual(
      new Map([
        [web.root, 'Web'],
        [ops.root, 'Ops'],
      ]),
    )
  })

  it('abbreviates the root when one project holds both', () => {
    const first = repo('shipflock', '/Users/rd/Projects/qa-1/shipflock')
    const second = repo('shipflock', '/Users/rd/Projects/qa-2/shipflock')
    const qa = { id: 'qa', name: 'QA', repos: [first.root, second.root] }

    expect(repoQualifiers([first, second], [qa])).toEqual(
      new Map([
        [first.root, '…/qa-1/shipflock'],
        [second.root, '…/qa-2/shipflock'],
      ]),
    )
  })

  it('deepens the abbreviation until the roots differ', () => {
    const work = repo('api', '/Users/rd/work/repos/api')
    const oss = repo('api', '/Users/rd/oss/repos/api')

    expect(repoQualifiers([work, oss], [])).toEqual(
      new Map([
        [work.root, '…/work/repos/api'],
        [oss.root, '…/oss/repos/api'],
      ]),
    )
  })

  it('abbreviates the root of a repo no project names', () => {
    const held = repo('api', '/Users/rd/web/api')
    const loose = repo('api', '/Users/rd/scratch/api')
    const web = { id: 'web', name: 'Web', repos: [held.root] }

    expect(repoQualifiers([held, loose], [web])).toEqual(
      new Map([
        [held.root, 'Web'],
        [loose.root, '…/scratch/api'],
      ]),
    )
  })
})

describe('expandedOnOpen', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]
  const platform = project('platform', 'Platform', ['bravo', 'charlie'])
  const rows = groupRepos(repos, [platform])

  it('opens the project holding the scoped repo and no other', () => {
    expect([...expandedOnOpen(rows, 'charlie')]).toEqual(['platform'])
  })

  it('leaves every project shut when nothing is scoped into one', () => {
    expect([...expandedOnOpen(rows, 'alpha')]).toEqual([])
    expect([...expandedOnOpen(rows, '')]).toEqual([])
  })

  it('leaves a single-member project shut — it has no row to open', () => {
    const lone = groupRepos(repos, [project('alpha', 'Alpha Only', ['alpha'])])
    expect([...expandedOnOpen(lone, 'alpha')]).toEqual([])
  })
})

describe('toggleExpanded', () => {
  it('opens a shut project and shuts an open one', () => {
    expect([...toggleExpanded(new Set(), 'platform')]).toEqual(['platform'])
    expect([...toggleExpanded(new Set(['platform']), 'platform')]).toEqual([])
  })

  it('leaves the other projects as they were', () => {
    expect([...toggleExpanded(new Set(['platform']), 'tools')]).toEqual([
      'platform',
      'tools',
    ])
  })

  it('hands back a new set, so React sees the change', () => {
    const expanded = new Set(['platform'])
    expect(toggleExpanded(expanded, 'tools')).not.toBe(expanded)
    expect([...expanded]).toEqual(['platform'])
  })
})

describe('projectAnchor', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]

  it('leaves a repo no project holds on itself', () => {
    expect(projectAnchor('bravo', repos, [])).toBe('bravo')
  })

  it('leaves a single-member project on its own repo', () => {
    expect(projectAnchor('bravo', repos, [project('bravo', 'bravo', ['bravo'])])).toBe('bravo')
  })

  it('anchors every member of a project on its first repo', () => {
    const platform = [project('platform', 'Platform', ['bravo', 'charlie'])]
    expect(projectAnchor('bravo', repos, platform)).toBe('bravo')
    expect(projectAnchor('charlie', repos, platform)).toBe('bravo')
  })

  it('skips a member the repos list no longer carries', () => {
    const platform = [project('platform', 'Platform', ['gone', 'charlie'])]
    expect(projectAnchor('charlie', repos, platform)).toBe('charlie')
  })
})

describe('removalPlan', () => {
  it('names the members that will go and every one the hub would keep', () => {
    const { removing, blocked } = removalPlan([
      repo('api'),
      { ...repo('web'), live: true },
      { ...repo('docs'), seeded: true },
    ])
    expect(removing).toEqual(['api'])
    expect(blocked.map((member) => member.name)).toEqual(['web', 'docs'])
    expect(blocked[0].reason).toMatch(/loop is live/)
    expect(blocked[1].reason).toMatch(/SERVE_WORKSPACE/)
  })
})

describe('removalSummary', () => {
  function removal(
    removed: string[],
    blocked: [name: string, reason: string][] = [],
  ): ProjectRemoval {
    return {
      project: project('acme', 'acme', [
        ...removed,
        ...blocked.map(([name]) => name),
      ]),
      removed: removed.map((name) => ({ name, root: `/repos/${name}` })),
      blocked: blocked.map(([name, reason]) => ({
        name,
        root: `/repos/${name}`,
        reason,
      })),
      project_deleted: blocked.length === 0,
    }
  }

  it('counts what left when the whole folder cleared', () => {
    expect(removalSummary(removal(['api', 'web', 'docs']))).toBe(
      '3 repos removed from acme',
    )
  })

  it('names every member that stayed and why', () => {
    const summary = removalSummary(
      removal(
        ['api'],
        [
          ['web', 'a loop is live here'],
          ['docs', 'granted by SERVE_WORKSPACE'],
        ],
      ),
    )
    expect(summary).toBe(
      '1 of 3 removed from acme — web stayed (a loop is live here), docs stayed (granted by SERVE_WORKSPACE)',
    )
  })
})

describe('projectMembers', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]

  it('offers no choice for a repo no project holds', () => {
    expect(projectMembers('bravo', repos, [])).toEqual({
      project: '',
      members: [],
    })
  })

  it('offers no choice for a single-member project', () => {
    const own = [project('bravo', 'bravo', ['bravo'])]
    expect(projectMembers('bravo', repos, own)).toEqual({
      project: 'bravo',
      members: [repos[1]],
    })
  })

  it('lists every member in project order, from any of them', () => {
    const platform = [project('platform', 'Platform', ['charlie', 'bravo'])]
    const want = { project: 'platform', members: [repos[2], repos[1]] }
    expect(projectMembers('bravo', repos, platform)).toEqual(want)
    expect(projectMembers('charlie', repos, platform)).toEqual(want)
  })

  it('drops a member the repos list no longer carries', () => {
    const platform = [project('platform', 'Platform', ['gone', 'charlie'])]
    expect(projectMembers('charlie', repos, platform)).toEqual({
      project: 'platform',
      members: [repos[2]],
    })
  })
})

describe('renameProject', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('patches the project with its new name', async () => {
    const fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({ id: 'p1', name: 'Acme Platform', repos: [] }),
    }))
    vi.stubGlobal('fetch', fetchMock)

    const renamed = await renameProject('p1', 'Acme Platform')

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/projects/p1')
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(String(init.body))).toEqual({ name: 'Acme Platform' })
    expect(renamed.name).toBe('Acme Platform')
  })

  it('reports the refusal of an empty name', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: false,
        status: 400,
        json: async () => ({ error: 'project name is empty' }),
      })),
    )

    await expect(renameProject('p1', '')).rejects.toThrow('project name is empty')
  })
})

describe('projectDefaults', () => {
  function tracker(keys: ProjectTracker['keys']): ProjectTracker {
    return { project: 'p1', repos: ['/repos/api'], keys }
  }

  it('reads the stored label and the epic flow the project holds', () => {
    expect(
      projectDefaults(
        tracker([
          { key: 'TRACKER_PROVIDER', value: 'linear' },
          { key: 'READY_LABEL', value: 'ship-it' },
          { key: 'EPIC_FLOW', value: '1' },
        ]),
      ),
    ).toEqual({ readyLabel: 'ship-it', epicFlow: true })
  })

  it('falls back to a blank label and the shipped flow default of on', () => {
    expect(projectDefaults(tracker([]))).toEqual({
      readyLabel: '',
      epicFlow: true,
    })
    expect(projectDefaults(undefined)).toEqual({
      readyLabel: '',
      epicFlow: true,
    })
  })

  it('reads a stored 0 as the flow being off', () => {
    expect(
      projectDefaults(tracker([{ key: 'EPIC_FLOW', value: '0' }])).epicFlow,
    ).toBe(false)
  })
})

describe('projectDefaultKeys', () => {
  it('writes the trimmed label and the flow as 1 or 0', () => {
    expect(
      projectDefaultKeys({ readyLabel: '  ship-it  ', epicFlow: true }),
    ).toEqual({ READY_LABEL: 'ship-it', EPIC_FLOW: '1' })
    expect(
      projectDefaultKeys({ readyLabel: 'ready-for-agent', epicFlow: false }),
    ).toEqual({ READY_LABEL: 'ready-for-agent', EPIC_FLOW: '0' })
  })

  it('leaves out the field the user did not edit', () => {
    expect(projectDefaultKeys({ readyLabel: 'ship-it' })).toEqual({
      READY_LABEL: 'ship-it',
    })
    expect(projectDefaultKeys({ epicFlow: false })).toEqual({ EPIC_FLOW: '0' })
    expect(projectDefaultKeys({})).toEqual({})
  })

  it('sends a blank label so a cleared field clears the project answer', () => {
    expect(projectDefaultKeys({ readyLabel: '   ' })).toEqual({
      READY_LABEL: '',
    })
  })
})

describe('projectDefaultsModified', () => {
  it('marks a project that answers either field itself', () => {
    const keys = [{ key: 'TRACKER_PROVIDER', value: 'linear' }]
    expect(projectDefaultsModified({ project: 'p1', repos: [], keys })).toBe(
      false,
    )
    expect(
      projectDefaultsModified({
        project: 'p1',
        repos: [],
        keys: [...keys, { key: 'EPIC_FLOW', value: '0' }],
      }),
    ).toBe(true)
  })
})

describe('matchesProjectDefaults', () => {
  it('keeps the section on a blank query', () => {
    expect(matchesProjectDefaults('')).toBe(true)
  })

  it('matches the keys and the words the fields are labelled with', () => {
    expect(matchesProjectDefaults('ready')).toBe(true)
    expect(matchesProjectDefaults('epic_flow')).toBe(true)
    expect(matchesProjectDefaults('epic flow')).toBe(true)
  })

  it('drops the section on an unrelated query', () => {
    expect(matchesProjectDefaults('budget')).toBe(false)
  })
})

describe('writeProjectTracker', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('puts only the edited keys to the project tracker', async () => {
    const fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      json: async () => ({
        project: 'p1',
        repos: ['/repos/api', '/repos/web'],
        keys: [{ key: 'READY_LABEL', value: 'ship-it' }],
      }),
    }))
    vi.stubGlobal('fetch', fetchMock)

    const saved = await writeProjectTracker(
      'p1',
      projectDefaultKeys({ readyLabel: 'ship-it', epicFlow: true }),
    )

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/projects/p1/tracker')
    expect(init.method).toBe('PUT')
    expect(JSON.parse(String(init.body))).toEqual({
      keys: { READY_LABEL: 'ship-it', EPIC_FLOW: '1' },
    })
    expect(saved.repos).toHaveLength(2)
  })
})
