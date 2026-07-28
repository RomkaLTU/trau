import { describe, expect, it } from 'vitest'

import type { RepoView } from '@/lib/instances'
import type { StartRepoHint } from '@/lib/projects'
import { startChoice } from '@/lib/start-repo'

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

function hint(
  suggested: string,
  reason: StartRepoHint['reason'],
  names: string[],
): StartRepoHint {
  return {
    project: 'platform',
    repos: names.map((name) => ({ name, root: `/repos/${name}` })),
    suggested,
    reason,
  }
}

describe('startChoice', () => {
  const members = [repo('api'), repo('image-service'), repo('web')]
  const every = ['api', 'image-service', 'web']

  it('opens on the suggestion, tagged, until a pick replaces it', () => {
    const named = hint('image-service', 'named', every)
    expect(startChoice('api', members, named, '')).toMatchObject({
      choose: true,
      target: 'image-service',
      suggested: 'image-service',
      picked: false,
    })
    expect(startChoice('api', members, named, 'web')).toMatchObject({
      target: 'web',
      picked: true,
    })
  })

  it('opens on a remembered repo without tagging it a suggestion', () => {
    expect(startChoice('api', members, hint('web', 'remembered', every), '')).toMatchObject({
      target: 'web',
      suggested: '',
    })
  })

  it('offers only the members the hint says can take the ticket', () => {
    const local = hint('image-service', 'first', ['image-service'])
    expect(startChoice('api', members, local, '')).toMatchObject({
      choose: false,
      members: [members[1]],
      target: 'image-service',
    })
  })
})
