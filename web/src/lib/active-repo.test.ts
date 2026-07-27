import { describe, expect, it } from 'vitest'

import {
  ALL_SCOPE,
  autoScopeTarget,
  parseRepoUsage,
  repoRouteAction,
  resolveScope,
  sortRepos,
} from '@/lib/active-repo'
import type { RepoView } from '@/lib/instances'
import type { RepoBadgeState } from '@/lib/overview'

function repo(name: string, live = false): RepoView {
  return {
    name,
    root: `/repos/${name}`,
    runs_dir: `/repos/${name}/runs`,
    live,
    allowed: true,
    registered: true,
    seeded: false,
  }
}

describe('resolveScope', () => {
  const repos = [repo('alpha'), repo('bravo', true), repo('charlie')]

  it('keeps the stored repo when it still exists', () => {
    expect(resolveScope(repos, 'charlie')).toEqual({
      scope: 'charlie',
      repo: 'charlie',
      isAll: false,
    })
  })

  it('honors an explicit "all" scope when several repos are registered', () => {
    expect(resolveScope(repos, ALL_SCOPE)).toEqual({
      scope: ALL_SCOPE,
      repo: null,
      isAll: true,
    })
  })

  it('auto-scopes a lone repo instead of gating on "all"', () => {
    expect(resolveScope([repo('solo')], ALL_SCOPE)).toEqual({
      scope: 'solo',
      repo: 'solo',
      isAll: false,
    })
  })

  it('auto-scopes to a live repo when the stored scope is gone', () => {
    expect(resolveScope(repos, 'deleted')).toEqual({
      scope: 'bravo',
      repo: 'bravo',
      isAll: false,
    })
  })

  it('auto-scopes to the first repo when none are live and nothing is stored', () => {
    expect(resolveScope([repo('alpha'), repo('charlie')], null)).toEqual({
      scope: 'alpha',
      repo: 'alpha',
      isAll: false,
    })
  })

  it('leaves repo null and the gate off when there are no repos', () => {
    expect(resolveScope([], 'anything')).toEqual({
      scope: ALL_SCOPE,
      repo: null,
      isAll: false,
    })
  })
})

describe('autoScopeTarget', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]

  it('picks the lone repo', () => {
    expect(autoScopeTarget([repo('solo')], null)).toBe('solo')
  })

  it('prefers the last-used repo when it still exists', () => {
    expect(autoScopeTarget(repos, 'bravo')).toBe('bravo')
  })

  it('returns null when the last-used repo is gone and there is a choice', () => {
    expect(autoScopeTarget(repos, 'deleted')).toBeNull()
  })

  it('returns null when there is no history and several repos exist', () => {
    expect(autoScopeTarget(repos, null)).toBeNull()
  })
})

describe('sortRepos', () => {
  const repos = [repo('alpha'), repo('bravo'), repo('charlie')]

  function badges(
    states: Record<string, RepoBadgeState>,
  ): Map<string, RepoBadgeState> {
    return new Map(Object.entries(states))
  }

  function names(sorted: RepoView[]): string[] {
    return sorted.map((r) => r.name)
  }

  it('pushes repos with a working loop to the top', () => {
    const sorted = sortRepos(repos, badges({ charlie: 'active' }), {})
    expect(names(sorted)).toEqual(['charlie', 'alpha', 'bravo'])
  })

  it('orders the rest by last usage, newest first', () => {
    const sorted = sortRepos(repos, badges({}), { alpha: 10, charlie: 20 })
    expect(names(sorted)).toEqual(['charlie', 'alpha', 'bravo'])
  })

  it('keeps a running repo above a more recently used idle one', () => {
    const sorted = sortRepos(repos, badges({ bravo: 'active' }), {
      alpha: 30,
      bravo: 1,
    })
    expect(names(sorted)).toEqual(['bravo', 'alpha', 'charlie'])
  })

  it('ranks a parked repo with the rest rather than as running', () => {
    const sorted = sortRepos(repos, badges({ alpha: 'parked' }), {
      charlie: 5,
    })
    expect(names(sorted)).toEqual(['charlie', 'alpha', 'bravo'])
  })

  it('leaves the input untouched', () => {
    const input = [...repos]
    sortRepos(input, badges({ charlie: 'active' }), {})
    expect(names(input)).toEqual(['alpha', 'bravo', 'charlie'])
  })
})

describe('parseRepoUsage', () => {
  it('reads a stored map of stamps', () => {
    expect(parseRepoUsage('{"alpha":7}')).toEqual({ alpha: 7 })
  })

  it('drops entries that are not stamps', () => {
    expect(parseRepoUsage('{"alpha":7,"bravo":"soon"}')).toEqual({ alpha: 7 })
  })

  it('falls back to an empty map on junk', () => {
    expect(parseRepoUsage(null)).toEqual({})
    expect(parseRepoUsage('[1,2]')).toEqual({})
    expect(parseRepoUsage('not json')).toEqual({})
  })
})

describe('repoRouteAction', () => {
  it('stays when the scope already matches the route repo', () => {
    expect(repoRouteAction('loop', 'loop', false)).toBe('stay')
    expect(repoRouteAction('loop', 'loop', true)).toBe('stay')
  })

  it('adopts the route repo before the scope has synced', () => {
    expect(repoRouteAction('loop', 'other', false)).toBe('adopt')
    expect(repoRouteAction('loop', null, false)).toBe('adopt')
  })

  it('leaves once a synced route sees the scope switch to another repo', () => {
    expect(repoRouteAction('loop', 'other', true)).toBe('leave')
  })

  it('leaves once a synced route sees the scope widen to all projects', () => {
    expect(repoRouteAction('loop', null, true)).toBe('leave')
  })
})
