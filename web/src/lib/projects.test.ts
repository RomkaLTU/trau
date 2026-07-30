import { describe, expect, it } from 'vitest'

import type { RepoView } from '@/lib/instances'
import {
  filterRepoRows,
  groupRepos,
  projectAnchor,
  projectMembers,
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
