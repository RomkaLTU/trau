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
  const web = project('web', 'Web', ['bravo', 'charlie'])
  const rows = groupRepos(repos, [web])

  it('hands back every row for a blank query', () => {
    expect(filterRepoRows(rows, '   ')).toEqual(rows)
  })

  it('matches a repo by name or by root, whatever the case', () => {
    const alpha = [{ project: null, repos: [repos[0]] }]
    expect(filterRepoRows(rows, 'ALPH')).toEqual(alpha)
    expect(filterRepoRows(rows, '/repos/alpha')).toEqual(alpha)
  })

  it('keeps every member of a group whose own name matches', () => {
    expect(filterRepoRows(rows, 'we')).toEqual([
      { project: web, repos: [repos[1], repos[2]] },
    ])
  })

  it('narrows a group to its matching members, in row order', () => {
    expect(filterRepoRows(rows, 'l')).toEqual([
      { project: null, repos: [repos[0]] },
      { project: web, repos: [repos[2]] },
    ])
  })

  it('drops a row nothing in it matches', () => {
    expect(filterRepoRows(rows, 'delta')).toEqual([])
  })
})

describe('projectAnchor', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]

  it('leaves a repo no project holds on itself', () => {
    expect(projectAnchor('bravo', repos, [])).toBe('bravo')
  })

  it('leaves a single-member project on its own repo', () => {
    expect(
      projectAnchor('bravo', repos, [project('bravo', 'bravo', ['bravo'])]),
    ).toBe('bravo')
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
