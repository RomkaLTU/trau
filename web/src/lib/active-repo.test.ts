import { describe, expect, it } from 'vitest'

import { resolveActiveRepo } from '@/lib/active-repo'
import type { RepoView } from '@/lib/instances'

function repo(name: string, live = false): RepoView {
  return {
    name,
    root: `/repos/${name}`,
    runs_dir: `/repos/${name}/runs`,
    live,
    allowed: true,
    registered: true,
  }
}

describe('resolveActiveRepo', () => {
  const repos = [repo('alpha'), repo('bravo', true), repo('charlie')]

  it('keeps the stored repo when it still exists', () => {
    expect(resolveActiveRepo(repos, 'charlie')).toBe('charlie')
  })

  it('falls back to a live repo when the stored one is gone', () => {
    expect(resolveActiveRepo(repos, 'deleted')).toBe('bravo')
  })

  it('falls back to the first repo when none are live', () => {
    expect(resolveActiveRepo([repo('alpha'), repo('charlie')], null)).toBe('alpha')
  })

  it('returns null when there are no repos', () => {
    expect(resolveActiveRepo([], 'anything')).toBeNull()
  })
})
