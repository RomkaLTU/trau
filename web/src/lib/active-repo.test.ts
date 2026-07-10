import { describe, expect, it } from 'vitest'

import { ALL_SCOPE, autoScopeTarget, resolveScope } from '@/lib/active-repo'
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
