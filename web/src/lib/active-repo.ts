import type { RepoView } from './instances'
import type { RepoBadgeState } from './overview'

type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

// SCOPE_KEY reuses the original active-repo key so an existing concrete selection
// keeps working; its value is either a repo root or the ALL_SCOPE sentinel, and a
// name stored by an older build still resolves.
// LAST_REPO_KEY remembers the last concrete repo so acting on a gated page from
// "All projects" can auto-scope back to it.
// USAGE_KEY holds the per-repo last-used stamps, kept apart from the recents
// history: recents is capped for display, so a repo the user works in daily can
// fall out of it and lose its place at the top of the switcher.
const SCOPE_KEY = 'trau.active-repo'
const LAST_REPO_KEY = 'trau.last-repo'
const USAGE_KEY = 'trau.repo-usage'

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
  if (scope && scope !== ALL_SCOPE) {
    store.setItem(LAST_REPO_KEY, scope)
    store.setItem(
      USAGE_KEY,
      JSON.stringify({
        ...parseRepoUsage(store.getItem(USAGE_KEY)),
        [scope]: Date.now(),
      }),
    )
  }
}

export function loadLastRepo(): string | null {
  return browserStore()?.getItem(LAST_REPO_KEY) ?? null
}

// RepoUsage maps a repo root to when the scope last landed on it.
export type RepoUsage = Record<string, number>

export function parseRepoUsage(raw: string | null): RepoUsage {
  if (!raw) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return {}
    if (Array.isArray(parsed)) return {}
    return Object.fromEntries(
      Object.entries(parsed).filter(([, at]) => typeof at === 'number'),
    )
  } catch {
    return {}
  }
}

export function loadRepoUsage(): RepoUsage {
  return parseRepoUsage(browserStore()?.getItem(USAGE_KEY) ?? null)
}

// sortRepos orders the switcher's project list: a repo with a working loop
// first — that is where the user's attention is — then most recently used, then
// by name so repos with no history keep a stable order.
export function sortRepos(
  repos: readonly RepoView[],
  badges: ReadonlyMap<string, { state: RepoBadgeState }>,
  usage: RepoUsage,
): RepoView[] {
  const rank = (repo: RepoView) =>
    badges.get(repo.name)?.state === 'active' ? 0 : 1
  return [...repos].sort(
    (a, b) =>
      rank(a) - rank(b) ||
      (usage[b.root] ?? 0) - (usage[a.root] ?? 0) ||
      a.name.localeCompare(b.name),
  )
}

// findRepo resolves a repo identifier: roots first — the only way to address one
// of two repos sharing a name, and roots take whatever shape the hub's OS gives
// them — then names, so an ident from a repo-bound route or an older build's
// stored scope still resolves.
export function findRepo(
  repos: readonly RepoView[],
  ident: string,
): RepoView | undefined {
  return (
    repos.find((r) => r.root === ident) ?? repos.find((r) => r.name === ident)
  )
}

export interface ResolvedScope {
  scope: string
  repo: string | null
  // root is the resolved repo's root, which is what tells two repos sharing a
  // name apart.
  root: string | null
  isAll: boolean
}

// resolveScope turns the stored scope and the live repo set into the active
// scope. A stored ident that still resolves to a registered repo, or a lone
// repo, resolves to that repo, so the operate pages are never needlessly gated —
// auto-scope is the bigger win over a dead link. "All projects" only sticks when
// it was the explicit choice and more than one repo is registered. With no repos
// the shell has nothing to scope: repo is null and the gate stays off so pages
// can show a register prompt instead.
export function resolveScope(
  repos: readonly RepoView[],
  stored: string | null,
): ResolvedScope {
  if (repos.length === 0) {
    return { scope: ALL_SCOPE, repo: null, root: null, isAll: false }
  }
  const held =
    stored && stored !== ALL_SCOPE ? findRepo(repos, stored) : undefined
  if (held) {
    return { scope: held.name, repo: held.name, root: held.root, isAll: false }
  }
  if (stored === ALL_SCOPE && repos.length > 1) {
    return { scope: ALL_SCOPE, repo: null, root: null, isAll: true }
  }
  const primary = repos.find((r) => r.live) ?? repos[0]
  return {
    scope: primary.name,
    repo: primary.name,
    root: primary.root,
    isAll: false,
  }
}

// autoScopeTarget picks the repo to jump to when the user acts on a gated page
// from "All projects": a lone repo, else the most recently used repo when it is
// still registered. It returns null when there's a genuine choice to make, so the
// caller opens the switcher instead of guessing.
export function autoScopeTarget(
  repos: readonly RepoView[],
  lastRepo: string | null,
): RepoView | null {
  if (repos.length === 1) return repos[0]
  if (!lastRepo) return null
  return findRepo(repos, lastRepo) ?? null
}

export type RepoRouteAction = 'stay' | 'adopt' | 'leave'

// repoRouteAction reconciles a repo-bound route (one with a $repo URL segment,
// e.g. a live run) with the active scope. Entering the route adopts its repo as
// the scope, so deep links set the project. "All projects" already spans every
// repo, so the route stays without narrowing the scope to it. Once the scope has
// caught up (synced), a scope pointing at another project means the user switched
// in the switcher, so the route yields instead of leaving a stale run on screen.
export function repoRouteAction(
  routeRepo: string,
  scopeRepo: string | null,
  isAll: boolean,
  synced: boolean,
): RepoRouteAction {
  if (isAll || scopeRepo === routeRepo) return 'stay'
  if (!synced) return 'adopt'
  return 'leave'
}
