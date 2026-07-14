import type { RepoView } from './instances'

type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

// SCOPE_KEY reuses the original active-repo key so an existing concrete selection
// keeps working; its value is now either a repo name or the ALL_SCOPE sentinel.
// LAST_REPO_KEY remembers the last concrete repo so acting on a gated page from
// "All projects" can auto-scope back to it.
const SCOPE_KEY = 'trau.active-repo'
const LAST_REPO_KEY = 'trau.last-repo'

// ALL_SCOPE is the sentinel scope that spans every repo. Operate pages are
// gated under it; observe pages that already read across repos keep working.
export const ALL_SCOPE = 'all'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadStoredScope(): string | null {
  return browserStore()?.getItem(SCOPE_KEY) ?? null
}

export function storeScope(scope: string | null): void {
  const store = browserStore()
  if (!store) return
  if (scope) store.setItem(SCOPE_KEY, scope)
  else store.removeItem(SCOPE_KEY)
  if (scope && scope !== ALL_SCOPE) store.setItem(LAST_REPO_KEY, scope)
}

export function loadLastRepo(): string | null {
  return browserStore()?.getItem(LAST_REPO_KEY) ?? null
}

export interface ResolvedScope {
  scope: string
  repo: string | null
  isAll: boolean
}

// resolveScope turns the stored scope and the live repo set into the active
// scope. A stored concrete repo (still registered) or a lone repo resolves to
// that repo, so the operate pages are never needlessly gated — auto-scope is the
// bigger win over a dead link. "All projects" only sticks when it was the
// explicit choice and more than one repo is registered. With no repos the shell
// has nothing to scope: repo is null and the gate stays off so pages can show a
// register prompt instead.
export function resolveScope(
  repos: readonly RepoView[],
  stored: string | null,
): ResolvedScope {
  if (repos.length === 0) {
    return { scope: ALL_SCOPE, repo: null, isAll: false }
  }
  if (stored && stored !== ALL_SCOPE && repos.some((r) => r.name === stored)) {
    return { scope: stored, repo: stored, isAll: false }
  }
  if (stored === ALL_SCOPE && repos.length > 1) {
    return { scope: ALL_SCOPE, repo: null, isAll: true }
  }
  const primary = repos.find((r) => r.live)?.name ?? repos[0].name
  return { scope: primary, repo: primary, isAll: false }
}

// autoScopeTarget picks the repo to jump to when the user acts on a gated page
// from "All projects": a lone repo, else the most recently used repo when it is
// still registered. It returns null when there's a genuine choice to make, so the
// caller opens the switcher instead of guessing.
export function autoScopeTarget(
  repos: readonly RepoView[],
  lastRepo: string | null,
): string | null {
  if (repos.length === 1) return repos[0].name
  if (lastRepo && repos.some((r) => r.name === lastRepo)) return lastRepo
  return null
}

export type RepoRouteAction = 'stay' | 'adopt' | 'leave'

// repoRouteAction reconciles a repo-bound route (one with a $repo URL segment,
// e.g. a live run) with the active scope. Entering the route adopts its repo as
// the scope, so deep links set the project. Once the scope has caught up (synced),
// a scope pointing elsewhere means the user switched projects in the switcher, so
// the route yields instead of leaving a stale run on screen. isAll resolves repo
// to null, which also counts as a switch away.
export function repoRouteAction(
  routeRepo: string,
  scopeRepo: string | null,
  synced: boolean,
): RepoRouteAction {
  if (scopeRepo === routeRepo) return 'stay'
  if (!synced) return 'adopt'
  return 'leave'
}
