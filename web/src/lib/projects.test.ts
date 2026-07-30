import { describe, expect, it } from 'vitest'

import type { RepoView } from '@/lib/instances'
import {
  filterRepoRows,
  groupRepos,
  projectAnchor,
  projectMembers,
  removalPlan,
  removalSummary,
  type ProjectRemoval,
  type ProjectView,
} from '@/lib/projects'

function repo(name: string): RepoView {
  return {
    name,
    root: `/repos/${name}`,
    runs_dir: `/repos/${name}/runs`,
    live: false,
    allowed: true,
    registered: true,
    seeded: false,
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
