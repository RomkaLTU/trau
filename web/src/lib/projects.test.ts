import { describe, expect, it } from 'vitest'

import type { RepoView } from '@/lib/instances'
import { groupRepos, type ProjectView } from '@/lib/projects'

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
